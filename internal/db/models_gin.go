package db

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/G-Node/gogs/internal/setting"
	"github.com/G-Node/libgin/libgin"
	"github.com/G-Node/libgin/libgin/annex"
	log "gopkg.in/clog.v1"
)

// StartIndexing sends an indexing request to the configured indexing service
// for a repository.
func StartIndexing(repo Repository) {
	go func() {
		if setting.Search.IndexURL == "" {
			log.Trace("Indexing not enabled")
			return
		}
		log.Trace("Indexing repository %d", repo.ID)
		ireq := libgin.IndexRequest{
			RepoID:   repo.ID,
			RepoPath: repo.FullName(),
		}
		data, err := json.Marshal(ireq)
		if err != nil {
			log.Error(2, "Could not marshal index request: %v", err)
			return
		}
		key := []byte(setting.Search.Key)
		encdata, err := libgin.EncryptString(key, string(data))
		if err != nil {
			log.Error(2, "Could not encrypt index request: %v", err)
		}
		req, err := http.NewRequest(http.MethodPost, setting.Search.IndexURL, strings.NewReader(encdata))
		if err != nil {
			log.Error(2, "Error creating index request")
		}
		client := http.Client{}
		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			log.Error(2, "Error submitting index request for [%d: %s]: %v", repo.ID, repo.FullName(), err)
			return
		}
	}()
}

// RebuildIndex sends all repositories to the indexing service to be indexed.
func RebuildIndex() error {
	indexurl := setting.Search.IndexURL
	if indexurl == "" {
		return fmt.Errorf("Indexing service not configured")
	}

	// collect all repo ID -> Path mappings directly from the DB
	repos := make(RepositoryList, 0, 100)
	if err := x.Find(&repos); err != nil {
		return fmt.Errorf("get all repos: %v", err)
	}
	log.Trace("Found %d repositories to index", len(repos))
	for _, repo := range repos {
		StartIndexing(*repo)
	}
	log.Trace("Rebuilding search index")
	return nil
}

func annexUninit(path string) {
	// walker sets the permission for any file found to 0600, to allow deletion
	var mode os.FileMode
	walker := func(path string, info os.FileInfo, err error) error {
		if info == nil {
			return nil
		}

		mode = 0660
		if info.IsDir() {
			mode = 0770
		}

		if err := os.Chmod(path, mode); err != nil {
			log.Error(3, "failed to change permissions on '%s': %v", path, err)
		}
		return nil
	}

	log.Trace("Uninit annex at '%s'", path)
	if msg, err := annex.Uninit(path); err != nil {
		log.Error(3, "uninit failed: %v (%s)", err, msg)
		if werr := filepath.Walk(path, walker); werr != nil {
			log.Error(3, "file permission change failed: %v", werr)
		}
	}
}

func annexSetup(path string) {
	log.Trace("Running annex add (with filesize filter) in '%s'", path)

	// Initialise annex in case it's a new repository
	if msg, err := annex.Init(path, "--version=7"); err != nil {
		log.Error(2, "Annex init failed: %v (%s)", err, msg)
		return
	}

	// Upgrade to v7 in case the directory was here before and wasn't cleaned up properly
	if msg, err := annex.Upgrade(path); err != nil {
		log.Error(2, "Annex upgrade failed: %v (%s)", err, msg)
		return
	}

	// Enable addunlocked for annex v7
	if msg, err := annex.SetAddUnlocked(path); err != nil {
		log.Error(2, "Failed to set 'addunlocked' annex option: %v (%s)", err, msg)
	}

	// Set MD5 as default backend
	if msg, err := annex.MD5(path); err != nil {
		log.Error(2, "Failed to set default backend to 'MD5': %v (%s)", err, msg)
	}

	// Set size filter in config
	if msg, err := annex.SetAnnexSizeFilter(path, setting.Repository.Upload.AnnexFileMinSize*annex.MEGABYTE); err != nil {
		log.Error(2, "Failed to set size filter for annex: %v (%s)", err, msg)
	}
}

func annexSync(path string) error {
	log.Trace("Synchronising annexed data")
	if msg, err := annex.ASync(path, "--content"); err != nil {
		// TODO: This will also DOWNLOAD content, which is unnecessary for a simple upload
		// TODO: Use gin-cli upload function instead
		log.Error(2, "Annex sync failed: %v (%s)", err, msg)
		return fmt.Errorf("git annex sync --content [%s]", path)
	}

	// run twice; required if remote annex is not initialised
	if msg, err := annex.ASync(path, "--content"); err != nil {
		log.Error(2, "Annex sync failed: %v (%s)", err, msg)
		return fmt.Errorf("git annex sync --content [%s]", path)
	}
	return nil
}
