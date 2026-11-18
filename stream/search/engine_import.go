package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Filing a finished download into the library.
//
// Hardlink first, copy if the two folders are on different filesystems --
// never move, because the client is still seeding from the original. Names
// follow the layout Plex, Jellyfin and Emby all match without help:
//
//	Films/The Matrix (1999)/The Matrix (1999) [1080p].mkv
//	Series/Severance (2022)/Season 01/Severance - S01E02 [1080p].mkv

var videoExt = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".m4v": true, ".ts": true, ".webm": true, ".mov": true,
}

const sampleCutoff = 200 << 20

type importFile struct {
	Path string
	Size int64
}

// mapDownloadPath turns the client's path into this process's path, and
// refuses anything that lands outside the download folder.
func mapDownloadPath(remote string, set engineSettings) (string, error) {
	local := remote
	if set.RemoteDownloadPath != "" && set.DownloadPath != "" {
		rp := filepath.Clean(set.RemoteDownloadPath)
		if rel, err := filepath.Rel(rp, filepath.Clean(remote)); err == nil && !strings.HasPrefix(rel, "..") {
			local = filepath.Join(set.DownloadPath, rel)
		}
	}
	if set.DownloadPath != "" {
		rel, err := filepath.Rel(filepath.Clean(set.DownloadPath), filepath.Clean(local))
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", fmt.Errorf("%s is outside the download folder %s — set the path mapping", remote, set.DownloadPath)
		}
	}
	if _, err := os.Stat(local); err != nil {
		return "", fmt.Errorf("cannot see %s from here — set Download path (and the client's path, if it differs)", local)
	}
	return local, nil
}

// videoFiles lists playable files under root (a file or a folder), largest
// first, without samples unless a sample is all there is.
func videoFiles(root string) ([]importFile, error) {
	var all []importFile
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !videoExt[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		all = append(all, importFile{Path: p, Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	var real []importFile
	for _, f := range all {
		if f.Size >= sampleCutoff || !strings.Contains(strings.ToLower(filepath.Base(f.Path)), "sample") {
			real = append(real, f)
		}
	}
	if len(real) == 0 {
		real = all
	}
	sort.Slice(real, func(i, j int) bool { return real[i].Size > real[j].Size })
	return real, nil
}

// safeName strips what no filesystem accepts and what could climb a path.
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return -1
		}
		return r
	}, s)
	s = strings.Trim(strings.TrimSpace(s), ".")
	if s == "" {
		return "Untitled"
	}
	return s
}

func folderName(title string, year int) string {
	if year > 0 {
		return fmt.Sprintf("%s (%d)", safeName(title), year)
	}
	return safeName(title)
}

func qualityTag(q string) string {
	if q == "" {
		return ""
	}
	return " [" + safeName(q) + "]"
}

func movieDest(root string, it engineItem, quality, ext string) string {
	folder := folderName(it.Title, it.Year)
	return filepath.Join(root, folder, folder+qualityTag(quality)+ext)
}

func episodeDest(root string, it engineItem, season, episode int, quality, ext string) string {
	return filepath.Join(root, folderName(it.Title, it.Year), "Season "+pad2(season),
		fmt.Sprintf("%s - S%sE%s%s%s", safeName(it.Title), pad2(season), pad2(episode), qualityTag(quality), ext))
}

// linkOrCopy places src at dst. An existing dst is left alone: a file already
// in the library is never overwritten by an import.
func linkOrCopy(src, dst string) error {
	if info, err := os.Stat(dst); err == nil {
		// A replay of a placement that already finished (the process stopped
		// before the state commit) is recognised, not refused.
		if samePlacement(src, dst, info) {
			return nil
		}
		return fmt.Errorf("%s already exists", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	removeStalePartials(dst)
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	// One temp file per attempt: an interrupted copy can never block a retry.
	out, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+enginePartial+"*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// staleCopyAge: a partial file untouched this long belongs to an abandoned
// copy (an active copy rewrites its mtime continuously).
const staleCopyAge = time.Hour

// enginePartial marks the engine's own temp copies. Cleanup removes only
// names it could have created, never a neighbour such as "x.mkv.partial-backup".
const enginePartial = ".yarrit-partial-"

func isEnginePartial(name, base string) bool {
	if name == base+".partial" { // the name an earlier build used
		return true
	}
	rest, ok := strings.CutPrefix(name, base+enginePartial)
	if !ok || rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func removeStalePartials(dst string) {
	dir, base := filepath.Dir(dst), filepath.Base(dst)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !isEnginePartial(e.Name(), base) {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > staleCopyAge {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// samePlacement reports whether dst is src already placed: the same file
// (a hardlink) or a byte-for-byte identical copy.
func samePlacement(src, dst string, dstInfo os.FileInfo) bool {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return false
	}
	if os.SameFile(srcInfo, dstInfo) {
		return true
	}
	if srcInfo.Size() != dstInfo.Size() {
		return false
	}
	return sameContents(src, dst)
}

// sameContents compares two files in full, a MiB at a time. Any read error
// counts as different: a replay is accepted only on proof.
func sameContents(a, b string) bool {
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()
	const chunk = 1 << 20
	ba, bb := make([]byte, chunk), make([]byte, chunk)
	end := func(err error) bool { return err == io.EOF || err == io.ErrUnexpectedEOF }
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if (ea != nil && !end(ea)) || (eb != nil && !end(eb)) {
			return false
		}
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false
		}
		if end(ea) || end(eb) {
			return end(ea) && end(eb)
		}
	}
}

type importResult struct {
	Placed  []string
	Skipped []string // files that matched no episode
	Failed  []string // matched episodes whose placement failed: retried later
	// FailedSlots are the episodes behind Failed.
	FailedSlots []wantSlot
}

// importDownload files one completed download for one library item. For a
// series, every episode file found is matched by its SxxEyy and placed.
func importDownload(local string, it *engineItem, grab engineGrab, set engineSettings) (importResult, error) {
	var res importResult
	files, err := videoFiles(local)
	if err != nil {
		return res, err
	}
	if len(files) == 0 {
		return res, errors.New("no video files in the download")
	}
	switch it.Kind {
	case "movie":
		if set.MovieRoot == "" {
			return res, errors.New("set a Films folder before imports can run")
		}
		f := files[0]
		dst := movieDest(set.MovieRoot, *it, grab.Quality, strings.ToLower(filepath.Ext(f.Path)))
		if err := linkOrCopy(f.Path, dst); err != nil {
			return res, err
		}
		it.HasFile, it.File, it.Quality = true, dst, grab.Quality
		res.Placed = append(res.Placed, dst)
	case "series":
		if set.SeriesRoot == "" {
			return res, errors.New("set a Series folder before imports can run")
		}
		for _, f := range files {
			p := parseRelease(filepath.Base(f.Path))
			season, episode := p.Season, p.Episode
			if season == 0 && grab.Episode > 0 && len(files) == 1 {
				season, episode = grab.Season, grab.Episode
			}
			ep := findEpisode(it, season, episode)
			if ep == nil {
				res.Skipped = append(res.Skipped, filepath.Base(f.Path))
				continue
			}
			dst := episodeDest(set.SeriesRoot, *it, season, episode, grab.Quality, strings.ToLower(filepath.Ext(f.Path)))
			if ep.HasFile && ep.File == dst {
				// Placed on an earlier attempt and recorded: no need to read
				// both files again while a sibling episode is being retried.
				if _, err := os.Stat(dst); err == nil {
					res.Placed = append(res.Placed, dst)
					continue
				}
			}
			if err := linkOrCopy(f.Path, dst); err != nil {
				res.Failed = append(res.Failed, filepath.Base(f.Path)+": "+err.Error())
				res.FailedSlots = append(res.FailedSlots, wantSlot{Season: season, Episode: episode})
				continue
			}
			ep.HasFile, ep.File, ep.Quality = true, dst, grab.Quality
			res.Placed = append(res.Placed, dst)
		}
		if len(res.Placed) == 0 && len(res.Failed) > 0 {
			return res, fmt.Errorf("could not place any episode: %s", strings.Join(res.Failed, "; "))
		}
		if len(res.Placed) == 0 {
			return res, fmt.Errorf("no episode in the download matched the series (%d skipped)", len(res.Skipped))
		}
	}
	return res, nil
}

func findEpisode(it *engineItem, season, episode int) *engineEpisode {
	for i := range it.Episodes {
		if it.Episodes[i].Season == season && it.Episodes[i].Episode == episode {
			return &it.Episodes[i]
		}
	}
	return nil
}
