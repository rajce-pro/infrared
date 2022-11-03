package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"io/ioutil"
	"path/filepath"

	"github.com/df-mc/atomic"
	"github.com/fsnotify/fsnotify"
	"github.com/imdario/mergo"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

type FileConfig struct {
	Directory string `json:"directory" yaml:"directory"`
	Watch     bool   `json:"watch" yaml:"watch"`
}

type file struct {
	FileConfig
	watcher *atomic.Value[*fsnotify.Watcher]
	logger  *zap.Logger
}

func NewFile(cfg FileConfig, logger *zap.Logger) Provider {
	return &file{
		FileConfig: cfg,
		watcher:    atomic.NewValue[*fsnotify.Watcher](nil),
		logger:     logger,
	}
}

func (p *file) Provide(dataCh chan<- Data) (Data, error) {
	data, err := p.readConfigData()
	if err != nil && !p.Watch {
		return Data{}, err
	}

	if p.Watch {
		go func() {
			if err := p.watch(dataCh); err != nil {
				p.logger.Error("failed while watching provider",
					zap.Error(err),
					zap.String("provider", data.Type.String()),
				)
			}
		}()
	}

	return data, nil
}

func (p *file) watch(dataCh chan<- Data) error {
	if p.watcher.Load() != nil {
		return errors.New("already watching")
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	p.watcher.Store(w)

	if err := w.Add(p.Directory); err != nil {
		return err
	}

	for {
		select {
		case e, ok := <-w.Events:
			if !ok {
				p.logger.Debug("Closing file watcher",
					zap.String("cause", "watcher event channel closed"),
					zap.String("dir", p.Directory),
				)
				return nil
			}

			if e.Op&fsnotify.Remove == fsnotify.Remove ||
				e.Op&fsnotify.Write == fsnotify.Write ||
				e.Op&fsnotify.Create == fsnotify.Create ||
				e.Op&fsnotify.Rename == fsnotify.Rename ||
				e.Op == fsnotify.Remove {
				data, err := p.readConfigData()
				if err != nil {
					continue
				}
				dataCh <- data
			}
		case err, ok := <-w.Errors:
			if !ok {
				p.logger.Debug("closing file watcher",
					zap.String("cause", "watcher error channel closed"),
					zap.String("dir", p.Directory),
				)
				return nil
			}

			p.logger.Error("error while watching directory",
				zap.Error(err),
				zap.String("dir", p.Directory),
			)
		}
	}
}

func (p file) Close() error {
	if p.watcher != nil {
		if err := p.watcher.Load().Close(); err != nil {
			return err
		}
	}
	return nil
}

func (p file) readConfigData() (Data, error) {
	cfg := map[string]any{}
	readConfig := func(path string, info fs.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}

		cfgData := map[string]any{}
		if err := ReadConfigFile(path, &cfgData); err != nil {
			p.logger.Error("failed to read config",
				zap.Error(err),
				zap.String("configPath", path),
			)
			return fmt.Errorf("could not read %s; %v", path, err)
		}

		return mergo.Merge(&cfg, cfgData, mergo.WithOverride)
	}

	if err := filepath.Walk(p.Directory, readConfig); err != nil {
		return Data{}, err
	}

	return Data{
		Type:   FileType,
		Config: cfg,
	}, nil
}

func ReadConfigFile(filename string, v any) error {
	bb, err := ioutil.ReadFile(filename)
	if err != nil {
		return err
	}

	ext := filepath.Ext(filename)[1:]
	switch ext {
	case "json":
		if err := json.Unmarshal(bb, v); err != nil {
			return err
		}
	case "yml", "yaml":
		if err := yaml.Unmarshal(bb, v); err != nil {
			return err
		}
	default:
		return errors.New("unsupported file type")
	}
	return nil
}
