package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// TagInfo is the metadata embedded into the media file itself.
type TagInfo struct {
	Title       string
	Date        string
	Description string
	Genres      []string
	IMDb        string
	TMDB        string
	Show        string
	Season      int
	Episode     int
	Cover       string // a JPEG to embed as the cover picture, if the container has a place for one
}

var errNoTagTool = errors.New("mkvpropedit/ffmpeg not found")

// WriteTags embeds metadata with mkvpropedit (Matroska, edited in place) or
// ffmpeg (other containers, remuxed without re-encoding).
func WriteTags(path string, t *TagInfo) error {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".mkv" || ext == ".mka" || ext == ".webm" {
		if tool, err := exec.LookPath("mkvpropedit"); err == nil {
			return tagMKV(tool, path, t)
		}
	}
	if tool, err := exec.LookPath("ffmpeg"); err == nil {
		return tagFFmpeg(tool, path, t)
	}
	return errNoTagTool
}

type mkvSimple struct {
	Name   string `xml:"Name"`
	String string `xml:"String"`
}

type mkvTags struct {
	XMLName xml.Name `xml:"Tags"`
	Tag     struct {
		Targets struct {
			TargetTypeValue int `xml:"TargetTypeValue"`
		} `xml:"Targets"`
		Simple []mkvSimple `xml:"Simple"`
	} `xml:"Tag"`
}

func tagMKV(tool, path string, t *TagInfo) error {
	var tags mkvTags
	tags.Tag.Targets.TargetTypeValue = 50
	add := func(name, value string) {
		if value != "" {
			tags.Tag.Simple = append(tags.Tag.Simple, mkvSimple{name, value})
		}
	}
	add("TITLE", t.Title)
	add("DATE_RELEASED", t.Date)
	add("SYNOPSIS", t.Description)
	add("GENRE", strings.Join(t.Genres, ", "))
	add("IMDB", t.IMDb)
	if t.TMDB != "" {
		add("TMDB", t.TMDB)
	}
	add("COLLECTION", t.Show)

	body, err := xml.MarshalIndent(tags, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "mk-tags-*.xml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	tmp.WriteString(xml.Header)
	tmp.Write(body)
	if err := tmp.Close(); err != nil {
		return err
	}
	args := []string{path, "--edit", "info", "--set", "title=" + t.Title, "--tags", "global:" + tmp.Name()}
	if t.Cover == "" {
		return runTool(tool, args...)
	}
	// The cover replaces an earlier one. mkvpropedit warns (and exits with
	// 1) when asked to delete one that is not there, so it is looked up.
	if mkvHasCover(path) {
		args = append(args, "--delete-attachment", "name:cover.jpg")
	}
	args = append(args, "--attachment-name", "cover.jpg", "--attachment-mime-type", "image/jpeg", "--add-attachment", t.Cover)
	return runTool(tool, args...)
}

// mkvHasCover asks mkvmerge whether the file already has a cover.jpg.
func mkvHasCover(path string) bool {
	tool, err := exec.LookPath("mkvmerge")
	if err != nil {
		return false
	}
	out, err := exec.Command(tool, "-J", path).Output()
	if err != nil {
		return false
	}
	var info struct {
		Attachments []struct {
			FileName string `json:"file_name"`
		} `json:"attachments"`
	}
	json.Unmarshal(out, &info)
	for _, a := range info.Attachments {
		if a.FileName == "cover.jpg" {
			return true
		}
	}
	return false
}

func tagFFmpeg(tool, path string, t *TagInfo) error {
	dir, base := filepath.Split(path)
	// The extension stays last: ffmpeg picks the container by it.
	tmp := filepath.Join(dir, ".mk-tmp-"+base)
	args := []string{"-nostdin", "-v", "error", "-y", "-i", path}
	switch ext := strings.ToLower(filepath.Ext(path)); {
	case t.Cover != "" && (ext == ".mp4" || ext == ".m4v" || ext == ".mov"):
		// An earlier cover is dropped (0:V is video that is not a picture),
		// the new one goes after the film.
		args = append(args, "-i", t.Cover, "-map", "0", "-map", "-0:v", "-map", "0:V", "-map", "1",
			"-c", "copy", "-disposition:v:1", "attached_pic", "-map_metadata", "0")
	default:
		args = append(args, "-map", "0", "-c", "copy", "-map_metadata", "0")
	}
	add := func(name, value string) {
		if value != "" {
			args = append(args, "-metadata", name+"="+value)
		}
	}
	add("title", t.Title)
	add("date", t.Date)
	add("comment", t.Description)
	add("genre", strings.Join(t.Genres, ", "))
	if t.Show != "" {
		add("show", t.Show)
		add("season_number", itoa(t.Season))
		add("episode_sort", itoa(t.Episode))
	}
	args = append(args, tmp)
	if err := runTool(tool, args...); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func runTool(tool string, args ...string) error {
	var out bytes.Buffer
	cmd := exec.Command(tool, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(out.String())
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
			msg = msg[i+1:]
		}
		return fmt.Errorf("%s: %v: %s", filepath.Base(tool), err, msg)
	}
	return nil
}
