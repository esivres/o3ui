package cli

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// deskletAssets bundles the Cinnamon desklet payload into the binary so
// `o3ui desklet install` is a single self-contained command — no
// hunting for shipped assets after the user moved the binary around.
//
//go:embed all:desklet
var deskletAssets embed.FS

const deskletUUID = "o3ui@esivres"

func runDesklet(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: o3ui desklet <install|uninstall|where>")
		return 2
	}
	switch args[0] {
	case "install":
		return installDesklet(stdout, stderr)
	case "uninstall":
		return uninstallDesklet(stdout, stderr)
	case "where":
		dst, _ := deskletInstallDir()
		fmt.Fprintln(stdout, dst)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown desklet subcommand %q\n", args[0])
		return 2
	}
}

func deskletInstallDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "cinnamon", "desklets", deskletUUID), nil
}

func installDesklet(stdout, stderr io.Writer) int {
	dst, err := deskletInstallDir()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Walk the embedded `desklet/o3ui@esivres/` tree and mirror it
	// onto disk. We use the embed FS directly so this works whether
	// the binary lives in /usr/local/bin or ~/.local/bin — there's no
	// "look next to the executable" hunt.
	root := "desklet/" + deskletUUID
	count := 0
	err = fs.WalkDir(deskletAssets, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := deskletAssets.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(out, data, 0o644); err != nil {
			return err
		}
		count++
		return nil
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "installed %d files to %s\n", count, dst)
	// Best-effort: if the user has already added the desklet to their
	// desktop, prefill cli_path with our own binary path so the
	// desklet works without them having to dig through Configure.
	if exe, err := os.Executable(); err == nil {
		if updated := patchCliPath(exe); updated > 0 {
			fmt.Fprintf(stdout, "updated cli_path in %d desklet instance config(s)\n", updated)
		}
	}
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "next steps:")
	fmt.Fprintln(stdout, "  1. open System Settings → Desklets")
	fmt.Fprintln(stdout, "  2. find \"OVPN3\" in the list and click + to add it to the desktop")
	fmt.Fprintln(stdout, "  3. (optional) right-click → Configure if cli_path needs override")
	return 0
}

// patchCliPath rewrites the cli_path value field in every existing
// Cinnamon desklet-instance config file under `spices/o3ui@esivres/`.
// Returns the number of files touched. Cinnamon stores per-instance
// settings as a JSON document mirroring the schema; we walk the dir,
// load each, update only that one field, and write back. Other keys
// (profile, compact) are preserved.
func patchCliPath(exe string) int {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0
	}
	dir := filepath.Join(home, ".config", "cinnamon", "spices", "o3ui@esivres")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	updated := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		// Generic decode so we don't trample fields Cinnamon adds in
		// future versions (it sprinkles __md5__ and such alongside).
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(b, &doc); err != nil {
			continue
		}
		raw, ok := doc["cli_path"]
		if !ok {
			continue
		}
		var node map[string]interface{}
		if err := json.Unmarshal(raw, &node); err != nil {
			continue
		}
		node["value"] = exe
		patched, err := json.Marshal(node)
		if err != nil {
			continue
		}
		doc["cli_path"] = patched
		out, err := json.MarshalIndent(doc, "", "    ")
		if err != nil {
			continue
		}
		if err := writeFileAtomic(p, out, 0o644); err != nil {
			continue
		}
		updated++
	}
	return updated
}

// writeFileAtomic writes data to a temp file in the same directory,
// fsyncs it, then renames over the destination. Required for files
// another process may be reading concurrently — here, the Cinnamon
// desklet's own settings store. A vanilla os.WriteFile could leave
// the target half-written if Cinnamon happens to read mid-write,
// which silently corrupts the user's profile/compact preferences.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

func uninstallDesklet(stdout, stderr io.Writer) int {
	dst, err := deskletInstallDir()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := os.Stat(dst); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stdout, "not installed")
		return 0
	}
	if err := os.RemoveAll(dst); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "removed %s\n", dst)
	fmt.Fprintln(stdout, "remember to also remove the desklet from your desktop via System Settings → Desklets")
	return 0
}
