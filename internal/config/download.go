package config

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DownloadProgress reports granular download progress.
type DownloadProgress struct {
	ModelName  string
	FileName   string
	FileIndex  int   // 1-based, e.g. 2 for 2nd file of 8
	TotalFiles int   // Total files in bundle (1 for single file)
	BytesRead  int64 // Current bytes downloaded for this file
	TotalBytes int64 // Total expected bytes for this file (-1 if unknown)
	Percent    float64
	SpeedBPS   float64 // Estimated bytes/second
	Done       bool
	Error      error
	Message    string
}

// AllAvailableModels returns every model supported for transcription or VAD.
func AllAvailableModels() []WhisperModel {
	var all []WhisperModel
	all = append(all, SileroVADModel())
	all = append(all, AvailableModels()...)
	all = append(all, AvailableONNXModels()...)
	all = append(all, AvailableNemotronModels()...)
	return all
}

// FindModel looks up an available model by name or filename (case-insensitive).
func FindModel(name string) *WhisperModel {
	name = strings.TrimSpace(strings.ToLower(name))
	for _, m := range AllAvailableModels() {
		if strings.ToLower(m.Name) == name ||
			strings.ToLower(m.FileName) == name ||
			strings.ToLower(strings.TrimSuffix(m.FileName, ".bin")) == name ||
			strings.ToLower(strings.TrimPrefix(m.FileName, "ggml-")) == name {
			return &m
		}
	}
	return nil
}

// DownloadModel downloads a WhisperModel into ~/.gleann/models with optional text callback.
func DownloadModel(model WhisperModel, onProgress func(string)) error {
	return DownloadModelWithProgress(model, func(p DownloadProgress) {
		if onProgress != nil && p.Message != "" {
			onProgress(p.Message)
		}
	})
}

// DownloadModelWithProgress downloads a WhisperModel with detailed progress updates.
func DownloadModelWithProgress(model WhisperModel, cb func(DownloadProgress)) error {
	dir := ModelsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create models dir: %w", err)
	}

	if model.IsBundle() {
		bundleDir := ModelPath(model.FileName)
		if err := os.MkdirAll(bundleDir, 0o755); err != nil {
			return fmt.Errorf("create bundle dir: %w", err)
		}

		totalFiles := len(model.BundleFiles)
		for idx, bf := range model.BundleFiles {
			fileIndex := idx + 1
			dest := filepath.Join(bundleDir, bf.Name)
			if _, err := os.Stat(dest); err == nil {
				if cb != nil {
					cb(DownloadProgress{
						ModelName:  model.DisplayName,
						FileName:   bf.Name,
						FileIndex:  fileIndex,
						TotalFiles: totalFiles,
						Done:       true,
						Message:    fmt.Sprintf("[%d/%d] %s already exists, skipping", fileIndex, totalFiles, bf.Name),
					})
				}
				continue
			}

			if cb != nil {
				cb(DownloadProgress{
					ModelName:  model.DisplayName,
					FileName:   bf.Name,
					FileIndex:  fileIndex,
					TotalFiles: totalFiles,
					Message:    fmt.Sprintf("[%d/%d] Downloading %s...", fileIndex, totalFiles, bf.Name),
				})
			}

			err := HTTPDownloadWithProgress(bf.URL, dest, func(read, total int64, speed float64) {
				if cb != nil {
					pct := 0.0
					if total > 0 {
						pct = float64(read) / float64(total) * 100
					}
					cb(DownloadProgress{
						ModelName:  model.DisplayName,
						FileName:   bf.Name,
						FileIndex:  fileIndex,
						TotalFiles: totalFiles,
						BytesRead:  read,
						TotalBytes: total,
						Percent:    pct,
						SpeedBPS:   speed,
					})
				}
			})
			if err != nil {
				return fmt.Errorf("%s: %w", bf.Name, err)
			}
		}
		return nil
	}

	dest := ModelPath(model.FileName)
	if _, err := os.Stat(dest); err == nil {
		if cb != nil {
			cb(DownloadProgress{
				ModelName:  model.DisplayName,
				FileName:   model.FileName,
				FileIndex:  1,
				TotalFiles: 1,
				Done:       true,
				Message:    fmt.Sprintf("%s already exists, skipping", model.FileName),
			})
		}
		return nil
	}

	if cb != nil {
		cb(DownloadProgress{
			ModelName:  model.DisplayName,
			FileName:   model.FileName,
			FileIndex:  1,
			TotalFiles: 1,
			Message:    fmt.Sprintf("Downloading %s (%s)...", model.DisplayName, model.Size),
		})
	}

	return HTTPDownloadWithProgress(model.URL, dest, func(read, total int64, speed float64) {
		if cb != nil {
			pct := 0.0
			if total > 0 {
				pct = float64(read) / float64(total) * 100
			}
			cb(DownloadProgress{
				ModelName:  model.DisplayName,
				FileName:   model.FileName,
				FileIndex:  1,
				TotalFiles: 1,
				BytesRead:  read,
				TotalBytes: total,
				Percent:    pct,
				SpeedBPS:   speed,
			})
		}
	})
}

// HTTPDownload fetches a URL and writes it to dest atomically via a .tmp file.
func HTTPDownload(url, dest string) error {
	return HTTPDownloadWithProgress(url, dest, nil)
}

// HTTPDownloadWithProgress fetches a URL with progress reporting and writes atomically to dest.
func HTTPDownloadWithProgress(url, dest string, progress func(read, total int64, speed float64)) error {
	client := &http.Client{
		Timeout: 0, // No global timeout for large model downloads
	}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	tmpFile := dest + ".tmp"
	f, err := os.Create(tmpFile)
	if err != nil {
		return err
	}

	total := resp.ContentLength
	var read int64
	buf := make([]byte, 64*1024) // 64 KB chunks
	start := time.Now()
	lastReport := time.Now()

	for {
		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := f.Write(buf[:n]); wErr != nil {
				f.Close()
				os.Remove(tmpFile)
				return wErr
			}
			read += int64(n)

			now := time.Now()
			if progress != nil && (now.Sub(lastReport) >= 150*time.Millisecond || rErr != nil) {
				elapsed := now.Sub(start).Seconds()
				speed := 0.0
				if elapsed > 0 {
					speed = float64(read) / elapsed
				}
				progress(read, total, speed)
				lastReport = now
			}
		}
		if rErr != nil {
			if rErr == io.EOF {
				break
			}
			f.Close()
			os.Remove(tmpFile)
			return rErr
		}
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpFile)
		return err
	}

	return os.Rename(tmpFile, dest)
}
