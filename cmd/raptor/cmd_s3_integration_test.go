//go:build integration

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"go.yaml.in/yaml/v3"

	"github.com/Helles01/raptor-panel/internal/db"
	"github.com/Helles01/raptor-panel/internal/secrets"
)

// fakeBucket is the S3 server of the test: it keeps what the panel asked of it.
type fakeBucket struct {
	mu       sync.Mutex
	requests []string
	bodies   map[string]string
	object   bool
}

func (f *fakeBucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	if r.URL.Query().Has("uploads") {
		key += "?uploads"
	}
	if r.URL.Query().Has("uploadId") {
		key += "?uploadId=" + r.URL.Query().Get("uploadId")
	}
	f.requests = append(f.requests, key)
	if f.bodies == nil {
		f.bodies = map[string]string{}
	}
	f.bodies[key] = string(body)
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIAE2EEXAMPLE/") && r.URL.Query().Get("X-Amz-Signature") == "" {
		w.WriteHeader(403)
		fmt.Fprint(w, `<Error><Code>AccessDenied</Code><Message>not signed</Message></Error>`)
		return
	}
	switch {
	case r.Method == "POST" && r.URL.Query().Has("uploads"):
		fmt.Fprint(w, `<InitiateMultipartUploadResult><UploadId>UP-E2E</UploadId></InitiateMultipartUploadResult>`)
	case r.Method == "POST" && r.URL.Query().Has("uploadId"):
		f.object = true
		fmt.Fprint(w, `<CompleteMultipartUploadResult/>`)
	case r.Method == "DELETE":
		f.object = false
		w.WriteHeader(204)
	case r.Method == "GET" && f.object:
		fmt.Fprint(w, "the archive")
	default:
		w.WriteHeader(404)
	}
}

func (f *fakeBucket) saw(request string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		if r == request {
			return true
		}
	}
	return false
}

// A whole backup on S3 through the panel that is running: the node is given addresses of the bucket, reports the end, and the owner downloads and
// deletes it. The bucket is a server of the test; the node is the test with the node's token.
func TestPanelBackupsOnS3(t *testing.T) {
	dsn := os.Getenv("RAPTOR_TEST_DSN")
	if dsn == "" {
		t.Skip("RAPTOR_TEST_DSN is not set")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	host, port, _ := strings.Cut(cfg.Addr, ":")
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	assets, err := filepath.Abs("../../web/public/assets")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(assets, "manifest.json")); err != nil {
		t.Skip("the frontend is not built (web/public/assets)")
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := probe.Addr().String()
	probe.Close()

	bucket := &fakeBucket{}
	s3srv := httptest.NewServer(bucket)
	defer s3srv.Close()

	vars := map[string]string{
		"RAPTOR_DB_HOST": host, "RAPTOR_DB_PORT": port, "RAPTOR_DB_NAME": cfg.DBName, "RAPTOR_DB_USER": cfg.User, "RAPTOR_DB_PASSWORD": cfg.Passwd,
		"RAPTOR_SECRET_KEY": key, "RAPTOR_APP_URL": "http://127.0.0.1", "RAPTOR_HTTP_LISTEN": listen, "RAPTOR_ASSETS_DIR": assets, "RAPTOR_LOG_FORMAT": "text",
		"RAPTOR_BACKUPS_DRIVER": "s3", "RAPTOR_BACKUPS_S3_ENDPOINT": s3srv.URL, "RAPTOR_BACKUPS_S3_REGION": "us-east-1", "RAPTOR_BACKUPS_S3_BUCKET": "bkt",
		"RAPTOR_BACKUPS_S3_ACCESS_KEY": "AKIAE2EEXAMPLE", "RAPTOR_BACKUPS_S3_SECRET_KEY": "e2e-secret-key-value", "RAPTOR_BACKUPS_S3_PATH_STYLE": "true",
		"RAPTOR_BACKUPS_MAX_PART_SIZE": "5242880", "RAPTOR_BACKUPS_PRESIGNED_MINUTES": "30",
		"RAPTOR_FEATURES_HIDE_ADMIN_ACTIVITY": "true",
	}

	ctx := context.Background()
	sqlDB, err := sql.Open("mysql", dsn+"&loc=UTC")
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0")
	for _, table := range []string{"users", "api_keys", "locations", "nodes", "nests", "eggs", "egg_variables", "settings", "raptor_secrets", "recovery_tokens", "servers", "subusers",
		"allocations", "sessions", "activity_logs", "activity_log_subjects", "raptor_outbox", "backups", "server_variables"} {
		if _, err := conn.ExecContext(ctx, "TRUNCATE TABLE `"+table+"`"); err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}
	conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1")
	conn.Close()
	if _, err := db.Migrate(ctx, sqlDB); err != nil {
		t.Fatal(err)
	}

	do := func(stdin string, args ...string) string {
		t.Helper()
		code, out, errOut := runCmd(t, stdin, vars, args...)
		if code != 0 {
			t.Fatalf("raptor %s = %d\n%s", strings.Join(args, " "), code, errOut)
		}
		return out
	}
	do("", "user", "create", "--email", "admin@example.com", "--username", "admin", "--first-name", "Ada", "--last-name", "Admin", "--admin")
	do("", "user", "create", "--email", "root2@example.com", "--username", "root2", "--first-name", "Rita", "--last-name", "Root", "--admin")
	accountKey := strings.TrimSpace(do("", "apikey", "create", "--user", "admin", "--description", "e2e"))
	do("", "location", "create", "--short", "eu.ams", "--long", "Amsterdam")
	nodeFile := do("", "node", "create", "--name", "Node 1", "--location-id", "1", "--fqdn", "node1.example.com", "--memory", "4096", "--disk", "50000")
	do("", "nest", "create", "--name", "Minecraft", "--author", "p@example.com")
	do(e2eEgg, "egg", "import", "--nest-id", "1", "-")

	var nodeCfg struct {
		TokenID string `yaml:"token_id"`
		Token   string `yaml:"token"`
	}
	if err := yaml.Unmarshal([]byte(nodeFile), &nodeCfg); err != nil || nodeCfg.TokenID == "" || nodeCfg.Token == "" {
		t.Fatalf("the node's configuration is not understood: %v\n%s", err, "(not shown: it holds the token)")
	}

	const serverUUID = "11111111-1111-4111-8111-111111111111"
	const backupUUID = "bbbbbbbb-0000-4000-8000-0000000000e2"
	sqlDB.Exec("INSERT INTO allocations (node_id, ip, port) VALUES (1, '10.0.0.1', 25565)")
	sqlDB.Exec("INSERT INTO servers (id, uuid, uuidShort, node_id, name, description, owner_id, memory, swap, disk, io, cpu, allocation_id, nest_id, egg_id, startup, image, backup_limit, database_limit, allocation_limit) "+
		"VALUES (1, ?, '11111111', 1, 'Survival', 'a server', 1, 1024, 0, 5000, 500, 100, 1, 1, 1, 'java -jar {{JAR}}', 'ghcr.io/example/yolks:java_21', 3, 2, 3)", serverUUID)
	sqlDB.Exec("UPDATE allocations SET server_id = 1 WHERE id = 1")
	// The row a backup has once the panel has started it: on S3, not finished. (Starting one needs a node that answers.)
	if _, err := sqlDB.Exec("INSERT INTO backups (server_id, uuid, name, ignored_files, disk, created_at, updated_at) VALUES (1, ?, 'Cloud', '', 's3', NOW(), NOW())", backupUUID); err != nil {
		t.Fatal(err)
	}

	logs := &lockedBuffer{}
	srvCtx, stop := context.WithCancel(ctx)
	ready, done := make(chan net.Addr, 1), make(chan error, 1)
	e := &env{stdin: strings.NewReader(""), stdout: io.Discard, stderr: logs, getenv: func(k string) string { return vars[k] }}
	go func() { done <- serve(srvCtx, e, "", func(a net.Addr) { ready <- a }) }()
	var base string
	select {
	case addr := <-ready:
		base = "http://" + addr.String()
	case err := <-done:
		t.Fatalf("the panel did not start: %v\n%s", err, logs)
	case <-time.After(30 * time.Second):
		t.Fatalf("the panel did not start in time\n%s", logs)
	}
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("the panel did not stop in time")
		}
	}()

	node := newClient(t, base)
	nodeAuth := map[string]string{"Authorization": "Bearer " + nodeCfg.TokenID + "." + nodeCfg.Token}
	owner := newClient(t, base)
	ownerAuth := map[string]string{"Authorization": "Bearer " + accountKey}

	// The node asks where to send a backup of 12 MiB: three parts of 5 MiB, in the bucket, signed.
	status, _, out := node.do("GET", "/api/remote/backups/"+backupUUID+"?size=12582912", nil, nodeAuth)
	if status != 200 {
		t.Fatalf("the upload addresses = %d\n%s", status, out)
	}
	var up struct {
		Parts    []string `json:"parts"`
		PartSize int64    `json:"part_size"`
	}
	if err := json.Unmarshal(out, &up); err != nil || len(up.Parts) != 3 || up.PartSize != 5242880 {
		t.Fatalf("answer = %s", out)
	}
	for i, raw := range up.Parts {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		if u.Host != strings.TrimPrefix(s3srv.URL, "http://") || u.Path != "/bkt/"+serverUUID+"/"+backupUUID+".tar.gz" || q.Get("partNumber") != fmt.Sprint(i+1) ||
			q.Get("uploadId") != "UP-E2E" || q.Get("X-Amz-Signature") == "" || q.Get("X-Amz-Expires") != "1800" {
			t.Errorf("address %d = %s", i+1, raw)
		}
		if strings.Contains(raw, "e2e-secret-key-value") {
			t.Errorf("the secret is in an address")
		}
	}
	if !bucket.saw("POST /bkt/" + serverUUID + "/" + backupUUID + ".tar.gz?uploads") {
		t.Errorf("the panel did not start the upload: %v", bucket.requests)
	}
	var kept sql.NullString
	sqlDB.QueryRow("SELECT upload_id FROM backups WHERE uuid = ?", backupUUID).Scan(&kept)
	if kept.String != "UP-E2E" {
		t.Errorf("the id kept on the backup = %q", kept.String)
	}

	// The node says it is done, with the tags of the parts.
	report := map[string]any{
		"successful": true, "checksum": "abc123", "checksum_type": "sha256", "size": 12582912,
		"parts": []map[string]any{{"etag": "\"p1\"", "part_number": 1}, {"etag": "\"p2\"", "part_number": 2}, {"etag": "\"p3\"", "part_number": 3}},
	}
	if status, _, out := node.do("POST", "/api/remote/backups/"+backupUUID, report, nodeAuth); status != 204 {
		t.Fatalf("the report = %d\n%s", status, out)
	}
	complete := "POST /bkt/" + serverUUID + "/" + backupUUID + ".tar.gz?uploadId=UP-E2E"
	if !bucket.saw(complete) || !strings.Contains(bucket.bodies[complete], "PartNumber") || strings.Count(bucket.bodies[complete], "<Part>") != 3 {
		t.Errorf("the upload was not put together: %v\n%s", bucket.requests, bucket.bodies[complete])
	}
	var successful bool
	var completed sql.NullTime
	sqlDB.QueryRow("SELECT is_successful, completed_at FROM backups WHERE uuid = ?", backupUUID).Scan(&successful, &completed)
	if !successful || !completed.Valid {
		t.Errorf("the backup is %v, completed %v", successful, completed)
	}
	// A second report changes nothing and does not touch the bucket again.
	before := len(bucket.requests)
	if status, _, _ := node.do("POST", "/api/remote/backups/"+backupUUID, report, nodeAuth); status != 400 || len(bucket.requests) != before {
		t.Errorf("a second report = %d, bucket requests %d -> %d", status, before, len(bucket.requests))
	}

	// The owner sees it, and is given an address of the bucket to download it from; the bucket serves it for that address.
	status, _, out = owner.do("GET", "/api/client/servers/11111111/backups/"+backupUUID+"/download", nil, ownerAuth)
	if status != 200 {
		t.Fatalf("download = %d\n%s", status, out)
	}
	var dl struct {
		Attributes struct {
			URL string `json:"url"`
		} `json:"attributes"`
	}
	if err := json.Unmarshal(out, &dl); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dl.Attributes.URL)
	if err != nil || u.Host != strings.TrimPrefix(s3srv.URL, "http://") || u.Path != "/bkt/"+serverUUID+"/"+backupUUID+".tar.gz" || u.Query().Get("X-Amz-Expires") != "300" {
		t.Fatalf("the download address = %s, %v", dl.Attributes.URL, err)
	}
	resp, err := http.Get(dl.Attributes.URL)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(data) != "the archive" {
		t.Errorf("the bucket answered %d %q", resp.StatusCode, data)
	}
	if strings.Contains(logs.String(), "X-Amz-Signature") || strings.Contains(logs.String(), "e2e-secret-key-value") {
		t.Errorf("a signed address or a key is in the log of the panel")
	}

	// Deleting it removes the object from the bucket and the backup from the list.
	if status, _, out := owner.do("DELETE", "/api/client/servers/11111111/backups/"+backupUUID, nil, ownerAuth); status != 204 {
		t.Fatalf("delete = %d\n%s", status, out)
	}
	if !bucket.saw("DELETE /bkt/" + serverUUID + "/" + backupUUID + ".tar.gz") {
		t.Errorf("the object was not deleted from the bucket: %v", bucket.requests)
	}
	var left int
	sqlDB.QueryRow("SELECT COUNT(*) FROM backups WHERE uuid = ? AND deleted_at IS NULL", backupUUID).Scan(&left)
	if left != 0 {
		t.Errorf("the backup is still there")
	}

	checkFeatureSwitches(t, sqlDB, owner, ownerAuth)
}

// A switch of the configuration reaches the handlers of the panel that runs: here the entries of administrators are left out of the log of a server, except those of the owner and the subusers, as in the reference.
func checkFeatureSwitches(t *testing.T, sqlDB *sql.DB, owner *client, ownerAuth map[string]string) {
	t.Helper()
	// Another administrator, who does not own the server, did something on it.
	res, err := sqlDB.Exec("INSERT INTO activity_logs (event, ip, description, actor_type, actor_id, properties, `timestamp`) VALUES ('server:power.start', '127.0.0.1', '', ?, 2, '{}', NOW())", `Pterodactyl\Models\User`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err := sqlDB.Exec("INSERT INTO activity_log_subjects (activity_log_id, subject_id, subject_type) VALUES (?, 1, ?)", id, `Pterodactyl\Models\Server`); err != nil {
		t.Fatal(err)
	}
	status, _, out := owner.do("GET", "/api/client/servers/11111111/activity", nil, ownerAuth)
	if status != 200 || strings.Contains(string(out), "server:power.start") || !strings.Contains(string(out), "server:backup.delete") {
		t.Errorf("the log of the server = %d\n%s", status, out)
	}
}
