//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
)

func TestPreCommitExportRejectsUnsafePaths(t *testing.T) {
	for _, name := range []string{"parent", "absolute", "symlink", "symlink-missing-parent", "git-config", "git-case", "git-nested", "git-hooks", "git-symlink", "git-dir-alias", "relative", "nested", "default"} {
		t.Run(name, func(t *testing.T) {
			isolateBeadsDirForTest(t)
			root := t.TempDir()
			repo := filepath.Join(root, "repo")
			beadsDir := filepath.Join(repo, ".beads")
			writeFileForWorktreeTest(t, filepath.Join(beadsDir, "metadata.json"), []byte("{}\n"))
			runGitForWorktreeTest(t, repo, "init", "-q")
			runGitForWorktreeTest(t, repo, "add", ".beads/metadata.json")
			t.Chdir(repo)
			t.Setenv("GIT_DIR", filepath.Join(repo, ".git"))
			git.ResetCaches()
			t.Cleanup(git.ResetCaches)
			t.Setenv("BEADS_DIR", beadsDir)
			initConfigForTest(t)
			outside := filepath.Join(root, "outside")
			writeFileForWorktreeTest(t, filepath.Join(outside, "sentinel"), []byte("unchanged"))
			writeFileForWorktreeTest(t, filepath.Join(repo, "outside"), []byte("unchanged"))
			path := "issues.jsonl"
			valid := false
			sentinels := []string{filepath.Join(outside, "sentinel"), filepath.Join(repo, "outside")}
			switch name {
			case "git-config":
				path = ".git/config"
			case "git-case":
				path = ".GIT/config"
			case "git-nested":
				path = "sub/.GIT/x"
			case "git-hooks":
				path = ".git/hooks/pre-commit"
			case "git-symlink", "git-dir-alias":
				gitDir := filepath.Join(repo, ".git")
				if name == "git-dir-alias" {
					gitDir = filepath.Join(repo, "git-admin")
					if err := os.Rename(filepath.Join(repo, ".git"), gitDir); err != nil {
						t.Fatal(err)
					}
					t.Setenv("GIT_DIR", gitDir)
					git.ResetCaches()
				}
				if err := os.Symlink(gitDir, filepath.Join(beadsDir, "git-link")); err != nil {
					t.Fatal(err)
				}
				path = "git-link/sentinel"
				sentinels = append(sentinels, filepath.Join(gitDir, "sentinel"))
			case "parent":
				path = "../outside"
			case "absolute":
				path = filepath.Join(outside, "sentinel")
			case "symlink", "symlink-missing-parent":
				if err := os.Symlink(outside, filepath.Join(beadsDir, "link")); err != nil {
					t.Fatal(err)
				}
				path = "link/sentinel"
				if name == "symlink-missing-parent" {
					path = "link/new/sentinel"
				}
			case "relative":
				path, valid = "./issues.jsonl", true
			case "nested":
				path, valid = "exports/new/issues.jsonl", true
			case "default":
				path, valid = "", true
			}
			if strings.HasPrefix(name, "git-") && name != "git-symlink" && name != "git-dir-alias" {
				sentinels = append(sentinels, filepath.Join(beadsDir, path))
			}
			for _, sentinel := range sentinels {
				writeFileForWorktreeTest(t, sentinel, []byte("unchanged"))
			}
			bin := filepath.Join(root, "bin")
			marker := filepath.Join(root, "called")
			stub := "#!/bin/sh\nprintf called > '" + marker + "'\nprintf exported > \"$3\"\n"
			writeFileForWorktreeTest(t, filepath.Join(bin, "bd"), []byte(stub))
			if err := os.Chmod(filepath.Join(bin, "bd"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			config.Set("export.auto", true)
			config.Set("export.git-add", false)
			config.Set("export.path", path)
			warning := captureHookStderr(t, exportJSONLForCommit)
			_, calledErr := os.Stat(marker)
			if valid {
				if calledErr != nil {
					t.Fatalf("export did not run: %v; %s", calledErr, warning)
				}
				if path == "" {
					path = "issues.jsonl"
				}
				if got := readFileForWorktreeTest(t, filepath.Join(beadsDir, path)); got != "exported" {
					t.Errorf("export = %q", got)
				}
			} else {
				if !os.IsNotExist(calledErr) {
					t.Error("unsafe export invoked bd")
				}
				if !strings.Contains(warning, "export.path") {
					t.Errorf("warning does not name export.path: %q", warning)
				}
				if _, err := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(err) {
					t.Error("created directory outside repository")
				}
			}
			for _, sentinel := range sentinels {
				if got := readFileForWorktreeTest(t, sentinel); got != "unchanged" {
					t.Errorf("sentinel changed: %q", got)
				}
			}
		})
	}
}

func TestMigrationLogAssetsStayConfined(t *testing.T) {
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	logDir := filepath.Join(beadsDir, "dolt")
	outside := filepath.Join(root, "sentinel")
	legit := filepath.Join(logDir, "server.log")
	link := filepath.Join(logDir, "server.log.1")
	other := filepath.Join(logDir, "unrelated")
	writeFileForWorktreeTest(t, outside, []byte("unchanged"))
	writeFileForWorktreeTest(t, legit, []byte("log"))
	writeFileForWorktreeTest(t, other, []byte("unchanged"))
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	j := &migrateJournal{LogAssets: []string{outside, link, legit, other}}
	warning := captureHookStderr(t, func() {
		if errs := removeMigrateAssets(logDir, j.LogAssets); len(errs) != 0 {
			t.Errorf("cleanup: %v", errs)
		}
	})
	if !strings.Contains(warning, "Warning") {
		t.Errorf("missing warning: %q", warning)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "unchanged" {
		t.Errorf("sentinel: %q, %v", data, err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("symlink removed: %v", err)
	}
	if _, err := os.Stat(legit); !os.IsNotExist(err) {
		t.Errorf("legitimate log not removed: %v", err)
	}
	if got := readFileForWorktreeTest(t, other); got != "unchanged" {
		t.Errorf("unrelated file changed: %q", got)
	}
	if errs := removeMigrateAssets(logDir, nil); len(errs) != 0 {
		t.Errorf("empty cleanup: %v", errs)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	writeFileForWorktreeTest(t, link, []byte("rotation"))
	if errs := removeMigrateAssets(logDir, []string{legit, link}); len(errs) != 0 {
		t.Errorf("rotation cleanup: %v", errs)
	}
	if _, err := os.Stat(link); !os.IsNotExist(err) {
		t.Errorf("rotation not removed: %v", err)
	}
}

func TestDatabaseMigrationRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	oldName := "../../victim/name-with-hyphen"
	newName := "../../victim/name_with_hyphen"
	victim := filepath.Join(root, "victim", "name-with-hyphen", "sentinel")
	writeFileForWorktreeTest(t, victim, []byte("unchanged"))
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := &configfile.Config{DoltDatabase: oldName}
	err := migrateHyphenatedDB(beadsDir, cfg, oldName, newName)
	if err == nil || !strings.Contains(err.Error(), "dolt_database") {
		t.Errorf("expected dolt_database error, got %v", err)
	}
	if data, err := os.ReadFile(victim); err != nil || string(data) != "unchanged" {
		t.Errorf("victim moved: %q, %v", data, err)
	}
	if cfg.DoltDatabase != oldName {
		t.Errorf("metadata modified: %q", cfg.DoltDatabase)
	}
}

func TestMetadataDatabaseNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "/absolute", `a\b`, "a/b", "bad\x00name", "../../victim/name-with-hyphen", "my-project", "my.project", "2026-notes", "2026.notes", strings.Repeat("a", 65), "beads"} {
		t.Run(name, func(t *testing.T) {
			beadsDir := t.TempDir()
			cfg := &configfile.Config{DoltDatabase: name}
			err := migrateHyphenatedDB(beadsDir, cfg, name, sanitizeDBName(name))
			valid := name == "my-project" || name == "my.project" || name == "2026-notes" || name == "2026.notes" || name == strings.Repeat("a", 65) || name == "beads"
			if valid {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "dolt_database") {
					t.Errorf("expected dolt_database error, got %v", err)
				}
				if _, err := os.Stat(filepath.Join(beadsDir, "metadata.json")); !os.IsNotExist(err) {
					t.Error("invalid name wrote metadata")
				}
			}
		})
	}
}

func TestHookExportPathResolvesRoot(t *testing.T) {
	isolateBeadsDirForTest(t)
	root := t.TempDir()
	runGitForWorktreeTest(t, root, "init", "-q")
	t.Chdir(root)
	t.Setenv("GIT_DIR", filepath.Join(root, ".git"))
	git.ResetCaches()
	t.Cleanup(git.ResetCaches)
	link := filepath.Join(t.TempDir(), "repo")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := confinedHookExportPath(filepath.Join(link, ".beads"), "nested/issues.jsonl", link); err != nil {
		t.Fatal(err)
	}
	if _, err := confinedHookExportPath(filepath.Join(root, ".beads"), "issues.jsonl", link); err != nil {
		t.Fatal(err)
	}
	if _, err := confinedHookExportPath(root, "issues.jsonl", ""); err == nil {
		t.Error("accepted unknown repository root")
	}
	writeFileForWorktreeTest(t, filepath.Join(root, "file"), []byte("test"))
	if _, err := confinedHookExportPath(root, "file/issues.jsonl", root); err == nil {
		t.Error("accepted non-directory parent")
	}
}

func TestMigrationLogAssetsRejectSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	logDir := filepath.Join(root, "dolt")
	sentinel := filepath.Join(outside, "server.log")
	writeFileForWorktreeTest(t, sentinel, []byte("unchanged"))
	if err := os.Symlink(outside, logDir); err != nil {
		t.Fatal(err)
	}
	warning := captureHookStderr(t, func() {
		if errs := removeMigrateAssets(logDir, []string{filepath.Join(logDir, "server.log")}); len(errs) != 0 {
			t.Errorf("cleanup: %v", errs)
		}
	})
	if !strings.Contains(warning, "Warning") {
		t.Errorf("missing warning: %q", warning)
	}
	if got := readFileForWorktreeTest(t, sentinel); got != "unchanged" {
		t.Errorf("sentinel changed: %q", got)
	}
}
