// Package r2 talks to the Cloudflare R2 bucket directly from the PC
// (S3-compatible API): session lock, folder upload/download, trash restore.
//
// Bucket layout under <prefix>/:
//
//	workspace/...        mirror of /workspace on the GPU box
//	trash/<UTC stamp>/   files rclone replaced or deleted during that sync
//	lock.json            which GPU session currently owns the workspace
package r2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/mozi30/Remote-GPU-Setup/internal/config"
	"github.com/mozi30/Remote-GPU-Setup/internal/filter"
)

const TrashStampLayout = "20060102T150405Z"

type Store struct {
	c      *minio.Client
	bucket string
	prefix string
}

func New(cfg *config.Config) (*Store, error) {
	secret := config.Secret(config.SecretR2)
	if cfg.R2AccountID == "" || cfg.R2AccessKey == "" || secret == "" || cfg.R2Bucket == "" {
		return nil, errors.New("R2 is not configured (Settings → Cloudflare R2)")
	}
	c, err := minio.New(cfg.R2Endpoint(), &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.R2AccessKey, secret, ""),
		Secure:       true,
		Region:       "auto",
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, err
	}
	return &Store{c: c, bucket: cfg.R2Bucket, prefix: strings.Trim(cfg.R2Prefix, "/")}, nil
}

func (s *Store) key(parts ...string) string {
	all := append([]string{s.prefix}, parts...)
	var nonEmpty []string
	for _, p := range all {
		if p = strings.Trim(p, "/"); p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	return strings.Join(nonEmpty, "/")
}

func (s *Store) Check(ctx context.Context) error {
	ok, err := s.c.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("R2: %w", err)
	}
	if !ok {
		return fmt.Errorf("R2 bucket %q does not exist", s.bucket)
	}
	return nil
}

// ---- session lock ----

type Lock struct {
	Provider   string    `json:"provider"`
	InstanceID string    `json:"instance_id"`
	PC         string    `json:"pc"`
	Since      time.Time `json:"since"`
}

func (s *Store) ReadLock(ctx context.Context) (*Lock, error) {
	obj, err := s.c.GetObject(ctx, s.bucket, s.key("lock.json"), minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	var l Lock
	if err := json.NewDecoder(obj).Decode(&l); err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, nil
		}
		return nil, err
	}
	return &l, nil
}

func (s *Store) WriteLock(ctx context.Context, l Lock) error {
	b, _ := json.MarshalIndent(l, "", "  ")
	_, err := s.c.PutObject(ctx, s.bucket, s.key("lock.json"), bytes.NewReader(b), int64(len(b)),
		minio.PutObjectOptions{ContentType: "application/json"})
	return err
}

// ReleaseLock removes the lock only if it belongs to instanceID.
func (s *Store) ReleaseLock(ctx context.Context, instanceID string) error {
	l, err := s.ReadLock(ctx)
	if err != nil || l == nil || l.InstanceID != instanceID {
		return err
	}
	return s.c.RemoveObject(ctx, s.bucket, s.key("lock.json"), minio.RemoveObjectOptions{})
}

// ---- listing helpers ----

type Object struct {
	Rel     string // relative to the listed root
	Size    int64
	ModTime time.Time
}

func (s *Store) list(ctx context.Context, root string) ([]Object, error) {
	pfx := root + "/"
	var out []Object
	for o := range s.c.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: pfx, Recursive: true}) {
		if o.Err != nil {
			return nil, o.Err
		}
		out = append(out, Object{Rel: strings.TrimPrefix(o.Key, pfx), Size: o.Size, ModTime: o.LastModified})
	}
	return out, nil
}

// Usage returns file count and bytes of the workspace and of the trash.
func (s *Store) Usage(ctx context.Context) (wsFiles int, wsBytes int64, trashBytes int64, err error) {
	ws, err := s.list(ctx, s.key("workspace"))
	if err != nil {
		return
	}
	for _, o := range ws {
		wsBytes += o.Size
	}
	tr, err := s.list(ctx, s.key("trash"))
	for _, o := range tr {
		trashBytes += o.Size
	}
	return len(ws), wsBytes, trashBytes, err
}

// TopLevel lists the first-level folders/files of the workspace.
func (s *Store) TopLevel(ctx context.Context) ([]string, error) {
	pfx := s.key("workspace") + "/"
	var out []string
	for o := range s.c.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: pfx}) {
		if o.Err != nil {
			return nil, o.Err
		}
		out = append(out, strings.TrimPrefix(o.Key, pfx))
	}
	sort.Strings(out)
	return out, nil
}

// ---- upload / download ----

type Progress func(done, total int, name string)

// rclone stores a file's mtime in the "mtime" user metadata (unix seconds);
// setting it keeps timestamps intact when the box pulls these files.
func mtimeMeta(t time.Time) string {
	return strconv.FormatFloat(float64(t.UnixNano())/1e9, 'f', 9, 64)
}

// Upload copies localDir into workspace/<dest>, skipping excluded paths and
// files that are already there with the same size and a newer copy in R2.
func (s *Store) Upload(ctx context.Context, localDir, dest string, f *filter.Filter, progress Progress) (sent, skipped int, err error) {
	root := s.key("workspace", dest)
	existing := map[string]Object{}
	if objs, err := s.list(ctx, root); err == nil {
		for _, o := range objs {
			existing[o.Rel] = o
		}
	}
	type job struct{ path, rel string }
	var jobs []job
	err = filepath.Walk(localDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(localDir, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		// filter relative to the workspace root so "/data/**" etc. still apply
		wsRel := strings.Trim(dest+"/"+rel, "/")
		if f.Excluded(wsRel, info.IsDir()) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if o, ok := existing[rel]; ok && o.Size == info.Size() && !info.ModTime().After(o.ModTime) {
			skipped++
			return nil
		}
		jobs = append(jobs, job{p, rel})
		return nil
	})
	if err != nil {
		return 0, skipped, err
	}
	var done atomic.Int64
	err = parallel(ctx, len(jobs), 8, func(i int) error {
		j := jobs[i]
		st, err := os.Stat(j.path)
		if err != nil {
			return err
		}
		_, err = s.c.FPutObject(ctx, s.bucket, root+"/"+j.rel, j.path, minio.PutObjectOptions{
			UserMetadata: map[string]string{"mtime": mtimeMeta(st.ModTime())},
		})
		if err != nil {
			return fmt.Errorf("%s: %w", j.rel, err)
		}
		if progress != nil {
			progress(int(done.Add(1)), len(jobs), j.rel)
		}
		return nil
	})
	return int(done.Load()), skipped, err
}

// Download copies workspace/<src> into localDir, skipping files that already
// exist locally with the same size and a newer timestamp.
func (s *Store) Download(ctx context.Context, src, localDir string, progress Progress) (got, skipped int, err error) {
	root := s.key("workspace", src)
	objs, err := s.list(ctx, root)
	if err != nil {
		return 0, 0, err
	}
	// a single file selected at the top level
	if len(objs) == 0 && src != "" {
		if st, err := s.c.StatObject(ctx, s.bucket, root, minio.StatObjectOptions{}); err == nil {
			objs = []Object{{Rel: "", Size: st.Size, ModTime: st.LastModified}}
		}
	}
	var todo []Object
	for _, o := range objs {
		local := filepath.Join(localDir, filepath.FromSlash(o.Rel))
		if o.Rel == "" {
			local = filepath.Join(localDir, filepath.Base(src))
		}
		if st, err := os.Stat(local); err == nil && st.Size() == o.Size && !o.ModTime.After(st.ModTime()) {
			skipped++
			continue
		}
		todo = append(todo, o)
	}
	var done atomic.Int64
	err = parallel(ctx, len(todo), 8, func(i int) error {
		o := todo[i]
		key, local := root+"/"+o.Rel, filepath.Join(localDir, filepath.FromSlash(o.Rel))
		if o.Rel == "" {
			key, local = root, filepath.Join(localDir, filepath.Base(src))
		}
		if strings.Contains(o.Rel, "..") {
			return fmt.Errorf("unsafe key %q", o.Rel)
		}
		if err := s.c.FGetObject(ctx, s.bucket, key, local, minio.GetObjectOptions{}); err != nil {
			return fmt.Errorf("%s: %w", o.Rel, err)
		}
		if st, err := s.c.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{}); err == nil {
			if v, err := strconv.ParseFloat(st.UserMetadata["Mtime"], 64); err == nil {
				t := time.Unix(0, int64(v*1e9))
				_ = os.Chtimes(local, t, t)
			}
		}
		if progress != nil {
			progress(int(done.Add(1)), len(todo), o.Rel)
		}
		return nil
	})
	return int(done.Load()), skipped, err
}

// ---- trash ----

type Snapshot struct {
	Stamp string
	Time  time.Time
	Files int
	Bytes int64
}

// Snapshots lists trash snapshots, newest first.
func (s *Store) Snapshots(ctx context.Context) ([]Snapshot, error) {
	objs, err := s.list(ctx, s.key("trash"))
	if err != nil {
		return nil, err
	}
	by := map[string]*Snapshot{}
	for _, o := range objs {
		stamp, _, ok := strings.Cut(o.Rel, "/")
		if !ok {
			continue
		}
		sn := by[stamp]
		if sn == nil {
			t, _ := time.Parse(TrashStampLayout, stamp)
			sn = &Snapshot{Stamp: stamp, Time: t}
			by[stamp] = sn
		}
		sn.Files++
		sn.Bytes += o.Size
	}
	var out []Snapshot
	for _, sn := range by {
		out = append(out, *sn)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Stamp > out[j].Stamp })
	return out, nil
}

func (s *Store) SnapshotFiles(ctx context.Context, stamp string) ([]Object, error) {
	return s.list(ctx, s.key("trash", stamp))
}

// RestoreInR2 copies files from a trash snapshot back into workspace/
// (server-side copy). A current version that would be overwritten is first
// moved into a new trash snapshot, so a restore can itself be undone.
// Only used when no GPU session is syncing.
func (s *Store) RestoreInR2(ctx context.Context, stamp string, rels []string) error {
	now := time.Now().UTC().Format(TrashStampLayout)
	cp := func(dst, src string) error {
		_, err := s.c.CopyObject(ctx,
			minio.CopyDestOptions{Bucket: s.bucket, Object: dst},
			minio.CopySrcOptions{Bucket: s.bucket, Object: src})
		return err
	}
	return parallel(ctx, len(rels), 8, func(i int) error {
		cur := s.key("workspace", rels[i])
		if _, err := s.c.StatObject(ctx, s.bucket, cur, minio.StatObjectOptions{}); err == nil {
			if err := cp(s.key("trash", now, rels[i]), cur); err != nil {
				return err
			}
		}
		return cp(cur, s.key("trash", stamp, rels[i]))
	})
}

// PruneTrash deletes snapshots older than days. Returns snapshots removed.
func (s *Store) PruneTrash(ctx context.Context, days int) (int, error) {
	if days <= 0 {
		return 0, nil
	}
	snaps, err := s.Snapshots(ctx)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	removed := 0
	for _, sn := range snaps {
		if sn.Time.IsZero() || sn.Time.After(cutoff) {
			continue
		}
		ch := make(chan minio.ObjectInfo)
		go func() {
			defer close(ch)
			for o := range s.c.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: s.key("trash", sn.Stamp) + "/", Recursive: true}) {
				if o.Err == nil {
					ch <- o
				}
			}
		}()
		for e := range s.c.RemoveObjects(ctx, s.bucket, ch, minio.RemoveObjectsOptions{}) {
			if e.Err != nil {
				return removed, e.Err
			}
		}
		removed++
	}
	return removed, nil
}

// parallel runs fn(0..n-1) with up to workers goroutines; first error wins.
func parallel(parent context.Context, n, workers int, fn func(int) error) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	idx := make(chan int)
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				if err := fn(i); err != nil {
					once.Do(func() { first = err; cancel() })
				}
			}
		}()
	}
loop:
	for i := 0; i < n; i++ {
		select {
		case idx <- i:
		case <-ctx.Done():
			break loop
		}
	}
	close(idx)
	wg.Wait()
	if first == nil {
		first = parent.Err() // user cancelled
	}
	return first
}
