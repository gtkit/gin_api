package config

import (
	"strings"
	"testing"
)

func TestNamedConnectionsDecodeIndependently(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_DATABASE_CONNECTIONS__REPORTS__PASSWORD", "reports-secret")
	t.Setenv("APP_REDIS_CONNECTIONS__JOBS__PASSWORD", "jobs-secret")
	cfg, err := loadFromReader(strings.NewReader(minimalConfigYAML() + `
database_connections:
  reports:
    dbtype: mysql
    host: reports.local
    port: "3307"
    name: reports
    username: reader
  archive:
    dbtype: mysql
    host: archive.local
    port: "3308"
    name: archive
    username: archiver
redis_connections:
  jobs:
    addr: jobs.local:6380
    dbs: [0, 3]
  sessions:
    addr: sessions.local:6381
    dbs: [1]
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.DatabaseConnections) != 2 || len(cfg.RedisConnections) != 2 {
		t.Fatal("named configuration missing")
	}
	reports := cfg.DatabaseConnections["reports"]
	if reports.Host != "reports.local" || reports.Port != "3307" || reports.Password != "reports-secret" || reports.EffectiveMaxOpenConn() != DefaultDBMaxOpenConn {
		t.Fatal("database configuration or defaults changed")
	}
	if cfg.RedisConnections["jobs"].Password != "jobs-secret" || cfg.RedisConnections["sessions"].Password != "" || cfg.DatabaseConnections["archive"].Password != "" {
		t.Fatal("named credentials were inherited or lost")
	}
}

func TestNamedConnectionsValidation(t *testing.T) {
	validDB := "{dbtype: mysql, host: reports.local, port: '3306', name: reports, username: reader}"
	for _, tc := range []struct{ name, yaml string }{
		{"reserved", "database_connections: {default: " + validDB + "}"},
		{"uppercase", "database_connections: {Reports: " + validDB + "}"},
		{"path separator", "database_connections: {reports.prod: " + validDB + "}"},
		{"empty", "database_connections: {'': " + validDB + "}"},
		{"too long", "database_connections: {" + strings.Repeat("a", 65) + ": " + validDB + "}"},
		{"incomplete database", "database_connections: {reports: {host: reports.local}}"},
		{"incomplete redis", "redis_connections: {jobs: {dbs: [0]}}"},
		{"redis missing dbs", "redis_connections: {jobs: {addr: 'jobs.local:6380'}}"},
		{"invalid pool", "redis_connections: {jobs: {addr: 'jobs.local:6380', dbs: [0], pool_size: 1, min_idle_conns: 2}}"},
		{"unknown field", "redis_connections: {jobs: {addr: 'jobs.local:6380', dbs: [0], unknown: true}}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loadFromReader(strings.NewReader(minimalConfigYAML() + "\n" + tc.yaml)); err == nil {
				t.Fatal("invalid named connection accepted")
			}
		})
	}
}
