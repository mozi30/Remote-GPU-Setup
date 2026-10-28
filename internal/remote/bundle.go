package remote

import (
	"embed"
	"fmt"
	"strings"

	"github.com/mozi30/Remote-GPU-Setup/internal/config"
)

// Dir is where the scripts, rclone config and markers live on the box.
// It is outside /workspace so none of it is ever synced to R2.
const Dir = "/opt/backpack"

//go:embed scripts/*.sh
var scripts embed.FS

// Bundle builds every file the box needs: scripts, settings, rclone config
// (with a bucket-scoped R2 token) and the exclude list.
func Bundle(cfg *config.Config, r2Secret string) ([]File, error) {
	entries, err := scripts.ReadDir("scripts")
	if err != nil {
		return nil, err
	}
	var files []File
	for _, e := range entries {
		b, err := scripts.ReadFile("scripts/" + e.Name())
		if err != nil {
			return nil, err
		}
		// embedded files keep the repo's line endings; bash needs LF
		b = []byte(strings.ReplaceAll(string(b), "\r\n", "\n"))
		files = append(files, File{Name: e.Name(), Mode: 0o700, Data: b})
	}

	base := fmt.Sprintf("r2:%s/%s", cfg.R2Bucket, strings.Trim(cfg.R2Prefix, "/"))
	env := fmt.Sprintf("R2_DEST=%q\nR2_TRASH=%q\nMAX_DELETE=%d\nAUTOSAVE_SECONDS=%d\n",
		base+"/workspace", base+"/trash", cfg.MaxDelete, cfg.AutosaveMinutes*60)
	files = append(files, File{Name: "backpack.env", Mode: 0o600, Data: []byte(env)})

	rc := fmt.Sprintf(`[r2]
type = s3
provider = Cloudflare
access_key_id = %s
secret_access_key = %s
endpoint = https://%s
acl = private
no_check_bucket = true
`, cfg.R2AccessKey, r2Secret, cfg.R2Endpoint())
	files = append(files, File{Name: "rclone.conf", Mode: 0o600, Data: []byte(rc)})

	files = append(files, File{Name: "excludes.txt", Mode: 0o600,
		Data: []byte(strings.Join(cfg.Excludes, "\n") + "\n")})
	return files, nil
}

// TarExcludes converts rclone-style excludes into GNU tar --exclude patterns
// (used when rescuing /workspace to the PC over ssh).
func TarExcludes(patterns []string) []string {
	var out []string
	for _, p := range patterns {
		core := strings.TrimSuffix(p, "/**")
		if strings.HasPrefix(core, "/") {
			out = append(out, "."+core) // "./data" = top level only
		} else {
			out = append(out, strings.TrimPrefix(core, "**/"))
		}
	}
	return out
}
