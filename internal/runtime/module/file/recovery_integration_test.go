//go:build integration

package file

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/module/file/application"
	"github.com/gtkit/gin-api/internal/optional/upload"
	filerepo "github.com/gtkit/gin-api/internal/repository/file"
	"github.com/gtkit/gouploader"
	"github.com/redis/go-redis/v9"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	recoveryDBEnv = "GIN_API_FILE_VERIFY_SCHEMA"
	crashRootEnv  = "GIN_API_FILE_CRASH_ROOT"
	crashIDEnv    = "GIN_API_FILE_CRASH_ID"
	crashRedisEnv = "GIN_API_FILE_CRASH_REDIS"
)

var disposableSchema = regexp.MustCompile(`^gin_api_verify_0407_[a-z0-9]+$`)

// The test connects only to an explicitly named disposable local schema.
// Create the tables from their actual migration files before running it.
func openRecoveryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := os.Getenv(recoveryDBEnv)
	if name == "" {
		t.Skip("disposable local MySQL schema is required")
	}
	if !disposableSchema.MatchString(name) {
		t.Fatal("invalid disposable schema name")
	}
	db, err := gorm.Open(gormmysql.Open("root@tcp(127.0.0.1:3306)/"+name+
		"?parseTime=true&loc=UTC&timeout=2s&readTimeout=5s&writeTimeout=5s"),
		&gorm.Config{SkipDefaultTransaction: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

type crashJournal struct{ *filerepo.Repository }

func (crashJournal) PublishOperation(context.Context, models.FileOperation, models.File) error {
	os.Exit(86)
	return nil
}

type deleteCrashJournal struct{ *filerepo.Repository }

func (deleteCrashJournal) SetOperationState(context.Context, models.FileOperation, string, string, time.Time) error {
	os.Exit(87)
	return nil
}

func integrationService(t *testing.T, db *gorm.DB, root string, crash bool) *application.Service {
	t.Helper()
	uploader, err := gouploader.New(gouploader.WithLocalStorage(root), gouploader.WithChunkTempDir(filepath.Join(root, "fixture-chunks")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(uploader.Close)
	repo := filerepo.New(db)
	var operations application.OperationRepository = repo
	if crash {
		operations = crashJournal{repo}
	}
	return integrationServiceWithJournal(t, uploader, repo, application.OperationOptions{
		Repository: operations, Namespace: strings.Repeat("a", 64),
		Session: func(context.Context, string) (application.ChunkSession, bool, error) {
			return application.ChunkSession{}, false, errors.New("unexpected session lookup")
		},
	})
}

func integrationServiceWithJournal(t *testing.T, uploader *gouploader.Uploader, repo *filerepo.Repository, options application.OperationOptions) *application.Service {
	t.Helper()
	options.MaxFileBytes = 1024
	options.AllowedTypes = []string{".txt"}
	service, err := application.New(NewTransfer(uploader), adapter{repo: repo}, "local", "", options)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func mergeFixture(t *testing.T, db *gorm.DB, root, redisAddr string, crash string) *application.Service {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: redisAddr})
	t.Cleanup(func() { _ = client.Close() })
	up, sessions, err := upload.NewRecoveryUploader(upload.UploaderOptions{
		Dir: filepath.Join(root, "files"), ChunkDir: filepath.Join(root, "chunks"), Redis: client,
		KeyPrefix: "verify-0407:", MaxFileSize: 1024, AllowedTypes: []string{".txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(up.Close)
	repo := filerepo.New(db)
	var journal application.OperationRepository = repo
	switch crash {
	case "merge":
		journal = crashJournal{repo}
	case "delete":
		journal = deleteCrashJournal{repo}
	}
	return integrationServiceWithJournal(t, up, repo, application.OperationOptions{
		Repository: journal, Namespace: sessions.Namespace(), Session: recoverySessionLookup(sessions),
	})
}

func TestIntegrationMergeAndDeleteRecoverAcrossProcessExit(t *testing.T) {
	db := openRecoveryTestDB(t)
	ctx := t.Context()
	const owner, other = 40721, 40722
	const body = "same file after merge and replay"
	if id, mode, child := strings.Cut(os.Getenv(crashIDEnv), ":"); child {
		svc := mergeFixture(t, db, os.Getenv(crashRootEnv), os.Getenv(crashRedisEnv), mode)
		if mode == "merge" {
			_, err := svc.Merge(ctx, owner, id)
			t.Fatalf("merge child did not exit at publish: %v", err)
		}
		t.Fatalf("delete child did not exit at completion: %v", svc.Delete(ctx, owner, id))
	}
	redisServer := miniredis.RunT(t)
	root := t.TempDir()
	runChild := func(id, mode string, want int) {
		t.Helper()
		childCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestIntegrationMergeAndDeleteRecoverAcrossProcessExit$")
		cmd.Env = append(os.Environ(), crashRootEnv+"="+root, crashIDEnv+"="+id+":"+mode, crashRedisEnv+"="+redisServer.Addr())
		output, err := cmd.CombinedOutput()
		exit, ok := errors.AsType[*exec.ExitError](err)
		if !ok || exit.ExitCode() != want {
			t.Fatalf("%s child exit=%v\n%s", mode, err, output)
		}
	}
	svc := mergeFixture(t, db, root, redisServer.Addr(), "")
	hash := sha256.Sum256([]byte(body))
	init, err := svc.ChunkInit(ctx, owner, &application.ChunkInitRequest{
		FileName: "merge.txt", FileSize: int64(len(body)), Checksum: hex.EncodeToString(hash[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadChunk(ctx, owner, init.UploadID, 0, strings.NewReader(body), int64(len(body)), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Merge(ctx, other, init.UploadID); !errors.Is(err, gouploader.ErrUnauthorized) {
		t.Fatalf("other account merge: %v", err)
	}
	runChild(init.UploadID, "merge", 86)
	restarted := mergeFixture(t, openRecoveryTestDB(t), root, redisServer.Addr(), "")
	expire := func(kind, businessKey string) {
		t.Helper()
		now := time.Now().UTC().Add(-time.Minute)
		result := db.WithContext(ctx).Model(&models.FileOperation{}).
			Where("owner_id = ? AND kind = ? AND business_key = ?", owner, kind, businessKey).
			Updates(map[string]any{"lease_until": now, "next_at": now})
		if result.Error != nil || result.RowsAffected != 1 {
			t.Fatalf("expire %s: rows=%d err=%v", kind, result.RowsAffected, result.Error)
		}
	}
	expire(models.FileMergeOperation, init.UploadID)
	if err := restarted.RecoverOperations(ctx); err != nil {
		t.Fatal(err)
	}
	one, err := restarted.Merge(ctx, owner, init.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	two, err := svc.Merge(ctx, owner, init.UploadID)
	if err != nil || two.ID != one.ID {
		t.Fatalf("merge replay changed file identity: %v", err)
	}
	runChild(one.ID, "delete", 87)
	if _, err := svc.Get(ctx, owner, one.ID); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("deleted row is visible: %v", err)
	}
	expire(models.FileDeleteOperation, one.ID)
	if err := restarted.RecoverOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Delete(ctx, owner, one.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Merge(ctx, owner, init.UploadID); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("replayed merge resurrected a deleted file: %v", err)
	}
	t.Log("two child processes exited at merge publication and delete completion, recovery preserved file identity and deletion")
}

func TestIntegrationKilledUploadIsRecoveredByOneInstance(t *testing.T) {
	db := openRecoveryTestDB(t)
	ctx := t.Context()
	const owner, otherOwner = 40701, 40702
	const payload = "recover this file after process exit"
	if root := os.Getenv(crashRootEnv); root != "" {
		service := integrationService(t, db, root, true)
		if _, err := service.Upload(ctx, owner, "original.txt", strings.NewReader(payload), int64(len(payload))); err != nil {
			t.Fatal(err)
		}
		t.Fatal("fault injection was not reached")
	}

	root := t.TempDir()
	childCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	command := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestIntegrationKilledUploadIsRecoveredByOneInstance$")
	command.Env = append(os.Environ(), crashRootEnv+"="+root)
	output, err := command.CombinedOutput()
	exit, ok := errors.AsType[*exec.ExitError](err)
	if !ok || exit.ExitCode() != 86 {
		t.Fatalf("child did not exit at the publication boundary: %v\n%s", err, output)
	}
	var op models.FileOperation
	if err := db.WithContext(ctx).Where("owner_id = ? AND kind = ?", owner, models.FileUploadOperation).Take(&op).Error; err != nil {
		t.Fatal(err)
	}
	if op.StorageKey == "" || op.State != models.FileOperationWorking {
		t.Fatalf("interrupted write was not registered: state=%s", op.State)
	}
	first := integrationService(t, db, root, false)
	second := integrationService(t, openRecoveryTestDB(t), root, false)
	if _, err := first.Get(ctx, owner, op.FileID); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("pre-publication visibility: %v", err)
	}
	// Advance only this fixture's lease without introducing a wall-clock wait.
	if err := db.WithContext(ctx).Model(&models.FileOperation{}).Where("id = ?", op.ID).
		Updates(map[string]any{"next_at": time.Now().UTC().Add(-time.Minute), "lease_until": time.Now().UTC().Add(-time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, service := range []*application.Service{first, second} {
		wg.Go(func() {
			if err := service.RecoverOperations(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var rows int64
	if err := db.WithContext(ctx).Model(&models.File{}).Where("owner_id = ? AND id = ?", owner, op.FileID).Count(&rows).Error; err != nil || rows != 1 {
		t.Fatalf("published rows=%d err=%v", rows, err)
	}
	_, rc, err := second.Download(ctx, owner, op.FileID)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := io.ReadAll(rc)
	if err := errors.Join(err, rc.Close()); err != nil || string(actual) != payload {
		t.Fatalf("recovered bytes=%d err=%v", len(actual), err)
	}
	if err := second.Delete(ctx, otherOwner, op.FileID); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("other account delete: %v", err)
	}
	if err := first.Delete(ctx, owner, op.FileID); err != nil {
		t.Fatal(err)
	}
	if err := second.Delete(ctx, owner, op.FileID); err != nil {
		t.Fatalf("repeated owner delete: %v", err)
	}
	t.Log("child exited immediately after object storage write, two instances competed for the operation, one file was published")
}

func TestIntegrationFileCursorTraversesMySQLHistory(t *testing.T) {
	db := openRecoveryTestDB(t)
	ctx := t.Context()
	const owner, foreignOwner = 40711, 40712
	const history = 10030
	stamp := time.Date(2026, time.November, 25, 1, 0, 0, 0, time.UTC)
	id := func(i int) string { return fmt.Sprintf("a0407000-0000-4000-8000-%012d", i) }
	files := make([]models.File, 0, history+10)
	for i := 1; i <= history+10; i++ {
		account := int64(owner)
		if i > history {
			account = foreignOwner
		}
		files = append(files, models.File{
			ID: id(i), OwnerID: account, FileName: "history.txt", StorageKey: fmt.Sprintf("history/%d", i),
			Driver: "local", CreatedAt: stamp, UpdatedAt: stamp,
		})
	}
	if err := db.WithContext(ctx).CreateInBatches(files, 500).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.WithContext(ctx).Model(&models.File{}).Where("owner_id = ? AND id IN ?", owner, []string{id(1), id(2)}).
		UpdateColumn("created_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	// Refresh optimizer statistics on only this disposable test table. Its
	// statistics are otherwise sampled asynchronously immediately after seeding.
	if err := db.WithContext(ctx).Exec("ANALYZE TABLE files").Error; err != nil {
		t.Fatal(err)
	}
	service := integrationService(t, db, t.TempDir(), false)
	seen := 0
	cursor := ""
	for {
		page, err := service.ListAfter(ctx, owner, cursor, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range page.Items {
			if record.ID != id(history-seen) || record.OwnerID != owner {
				t.Fatalf("unexpected row at position %d for owner %d", seen, record.OwnerID)
			}
			seen++
		}
		if cursor == "" {
			if _, err := service.ListAfter(ctx, foreignOwner, page.NextCursor, 1); !errors.Is(err, application.ErrInvalid) {
				t.Fatalf("cross-account cursor: %v", err)
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if seen != history {
		t.Fatalf("traversed %d, want %d", seen, history)
	}
	// Explicitly resume within the NULL tail, independent of the previous page size.
	beforeNull, err := service.ListAfter(ctx, owner, "", 1)
	if err != nil || len(beforeNull.Items) != 1 {
		t.Fatalf("one-row page: %v", err)
	}
	repo := filerepo.New(db)
	tail, err := repo.ListAfter(ctx, owner, time.Time{}, id(2), 1)
	if err != nil || len(tail) != 1 || !tail[0].CreatedAt.IsZero() || tail[0].ID != id(1) {
		t.Fatalf("NULL cursor tail = %v, err=%v", tail, err)
	}
	page, err := service.List(ctx, owner, 101, 100)
	if err != nil || page.Paging.TotalCount != history || len(page.Items) != 30 {
		t.Fatalf("deep page: total=%d len=%d err=%v", page.Paging.TotalCount, len(page.Items), err)
	}
	base := "EXPLAIN SELECT * FROM files FORCE INDEX (idx_files_owner) WHERE owner_id = ? AND deleted_at IS NULL "
	for _, query := range []struct {
		name, seek, order string
		args              []any
	}{
		{"first", "", "owner_id DESC,created_at DESC,id DESC", []any{owner, 101}},
		{"seek", "AND (created_at < ? OR (created_at = ? AND id < ?) OR created_at IS NULL) ", "owner_id DESC,created_at DESC,id DESC", []any{owner, stamp, stamp, id(5001), 101}},
		{"null tail", "AND created_at IS NULL AND id < ? ", "owner_id DESC,id DESC", []any{owner, id(2), 101}},
	} {
		var plan struct{ Key, Extra string }
		if err := db.WithContext(ctx).Raw(base+query.seek+"ORDER BY "+query.order+" LIMIT ?", query.args...).Scan(&plan).Error; err != nil {
			t.Fatal(err)
		}
		if plan.Key != "idx_files_owner" || strings.Contains(plan.Extra, "filesort") {
			t.Fatalf("%s unexpected execution plan: %+v", query.name, plan)
		}
		t.Logf("%s cursor plan: index=%s extra=%s", query.name, plan.Key, plan.Extra)
	}
	t.Logf("traversed %d owner records, excluded 10 foreign records, deep page total=%d", seen, page.Paging.TotalCount)
}
