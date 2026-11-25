//go:build optional && integration

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/models"
	fileapp "github.com/gtkit/gin-api/internal/module/file/application"
	"github.com/gtkit/gin-api/internal/pkg/ginc"
	"github.com/gtkit/gin-api/internal/platform/config"
	platformmysql "github.com/gtkit/gin-api/internal/platform/mysql"
	filerepo "github.com/gtkit/gin-api/internal/repository/file"
	fileruntime "github.com/gtkit/gin-api/internal/runtime/module/file"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
	"github.com/gtkit/gouploader"
	"gorm.io/gorm"
)

// TestLocalFileList exercises real MySQL and HTTP. Inserted fixture records
// live only inside a rolled-back transaction. Concurrent HTTP requests use a
// synthetic principal with an empty listing on the real connection pool.
func TestLocalFileList(t *testing.T) {
	if os.Getenv("FILE_LIST_MYSQL_INTEGRATION") != "1" {
		t.Skip("opt-in local files schema test")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	switch cfg.Database.Host {
	case "127.0.0.1", "::1", "localhost":
	default:
		t.Fatal("requires a loopback MySQL instance")
	}
	client, err := platformmysql.OpenDB(t.Context(), cfg.Database, cfg.Database.RuntimeCredentials(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	db := client.DB()
	owner := time.Now().UnixNano()
	rollback := errors.New("roll back file fixtures")
	err = db.Transaction(func(tx *gorm.DB) error {
		repo := filerepo.New(tx)
		id1, id2 := uuid.New().String(), uuid.New().String()
		if id1 > id2 {
			id1, id2 = id2, id1
		}
		at := time.Now().UTC().Truncate(time.Second)
		for _, id := range []string{id1, id2} {
			file := models.File{ID: id, OwnerID: owner, FileName: "fixture", StorageKey: id, Driver: "local", CreatedAt: at, UpdatedAt: at}
			if _, err := repo.Create(t.Context(), file); err != nil {
				return err
			}
		}
		first, err := repo.ListAfter(t.Context(), owner, time.Time{}, "", 1)
		if err != nil {
			return err
		}
		if len(first) != 2 || first[0].ID != id2 {
			return errors.New("unexpected first seek page")
		}
		next, err := repo.ListAfter(t.Context(), owner, first[0].CreatedAt, id2, 1)
		if err != nil {
			return err
		}
		if len(next) != 1 || next[0].ID != id1 {
			return errors.New("tie-breaker lost a row")
		}
		other, err := repo.ListAfter(t.Context(), owner-1, time.Time{}, "", 1)
		if err != nil {
			return err
		}
		if len(other) != 0 {
			return errors.New("owner scope failed")
		}
		deep, total, err := repo.List(t.Context(), owner, 100, 1)
		if err != nil {
			return err
		}
		if len(deep) != 0 || total != 2 {
			return errors.New("deep page total changed")
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}

	up, err := gouploader.New(gouploader.WithLocalStorage(t.TempDir()), gouploader.WithChunkTempDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(up.Close)
	svc, err := fileruntime.New(up, db, "local", "", time.Second*2)
	if err != nil {
		t.Fatal(err)
	}
	inf := infra{cfg: &config.Config{Env: "test"}}
	installMetricsFixture(t, &inf)
	engine, err := buildEngine(inf, services{optionalServices: optionalServices{file: svc}}, func(c *gin.Context) {
		ginc.SetUserID(c, owner)
		ginc.SetPrincipal(c, sharedauth.Principal{UserID: owner})
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(engine)
	defer server.Close()
	const workers, calls = 24, 40
	transport := &http.Transport{MaxIdleConnsPerHost: workers, MaxConnsPerHost: workers}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	path := server.URL + "/api/v1/files?cursor=&page_size=20"
	call := func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		r, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		var body struct {
			Code *int               `json:"code"`
			Data fileapp.CursorPage `json:"data"`
		}
		err = errors.Join(json.NewDecoder(r.Body).Decode(&body), r.Body.Close())
		if err != nil {
			return err
		}
		if r.StatusCode != 200 || body.Code == nil || *body.Code != 0 || body.Data.Items == nil || len(body.Data.Items) != 0 {
			return fmt.Errorf("unexpected file response with HTTP %d", r.StatusCode)
		}
		return nil
	}
	if err := call(t.Context()); err != nil {
		t.Fatal(err)
	}
	samples := make(chan time.Duration, workers*calls)
	var wg sync.WaitGroup
	start := time.Now()
	for range workers {
		wg.Go(func() {
			for range calls {
				then := time.Now()
				if err := call(t.Context()); err != nil {
					t.Error(err)
					return
				}
				samples <- time.Since(then)
			}
		})
	}
	wg.Wait()
	elapsed := time.Since(start)
	close(samples)
	var latencies []time.Duration
	for d := range samples {
		latencies = append(latencies, d)
	}
	if len(latencies) != workers*calls {
		t.Fatalf("successful requests %d/%d", len(latencies), workers*calls)
	}
	slices.Sort(latencies)
	t.Logf("loopback files cursor, synthetic identity, empty MySQL result, workers=%d successes=%d elapsed=%s RPS=%.0f p95=%s p99=%s", workers, len(latencies), elapsed, float64(len(latencies))/elapsed.Seconds(), latencies[(len(latencies)*95+99)/100-1], latencies[(len(latencies)*99+99)/100-1])
}
