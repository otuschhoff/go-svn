package client

import (
	"context"
	"path"
	"path/filepath"
	"strings"

	"github.com/otuschhoff/go-svn/mergeinfo"
	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/wc"
)

type WCMergeinfo struct {
	Mergeinfo mergeinfo.Mergeinfo
	Path      string
	Inherited bool
}

func (client *Client) WorkingCopyMergeinfo(ctx context.Context, targetPath string, inheritance mergeinfo.Inheritance) (*WCMergeinfo, error) {
	database, err := wc.Open(ctx, targetPath, wc.Options{})
	if err != nil {
		return nil, err
	}
	defer database.Close()
	root := database.RootPath()
	target, err := filepath.Abs(targetPath)
	if err != nil {
		return nil, err
	}
	for current := target; ; current = filepath.Dir(current) {
		info, infoErr := database.Info(ctx, current)
		if infoErr == nil {
			if value := info.WorkingProperties[props.Mergeinfo]; len(value) != 0 {
				parsed, err := mergeinfo.Parse(string(value))
				if err != nil {
					return nil, err
				}
				inherited := current != target
				if inherited {
					relative, err := filepath.Rel(current, target)
					if err != nil {
						return nil, err
					}
					parsed = adjustInheritedMergeinfo(parsed, filepath.ToSlash(relative), inheritance == mergeinfo.InheritanceInherited)
				}
				return &WCMergeinfo{Mergeinfo: parsed, Path: current, Inherited: inherited}, nil
			}
		}
		if inheritance == mergeinfo.InheritanceExplicit || current == root {
			break
		}
	}
	return &WCMergeinfo{Mergeinfo: make(mergeinfo.Mergeinfo), Path: target}, nil
}

func adjustInheritedMergeinfo(info mergeinfo.Mergeinfo, suffix string, inheritableOnly bool) mergeinfo.Mergeinfo {
	result := make(mergeinfo.Mergeinfo)
	for source, ranges := range info {
		if inheritableOnly {
			ranges = mergeinfo.Inheritable(ranges)
		}
		if len(ranges) != 0 {
			result[path.Join(source, suffix)] = append(mergeinfo.Rangelist(nil), ranges...)
		}
	}
	return result
}

func (client *Client) SetWorkingCopyMergeinfo(ctx context.Context, targetPath string, value mergeinfo.Mergeinfo, elide bool) error {
	database, err := wc.Open(ctx, targetPath, wc.Options{Writable: true})
	if err != nil {
		return err
	}
	defer database.Close()
	if elide {
		parent := filepath.Dir(targetPath)
		if parent != targetPath && strings.HasPrefix(parent, database.RootPath()) {
			inherited, err := client.WorkingCopyMergeinfo(ctx, parent, mergeinfo.InheritanceInherited)
			if err == nil {
				adjusted := adjustInheritedMergeinfo(inherited.Mergeinfo, filepath.ToSlash(filepath.Base(targetPath)), true)
				if mergeinfo.String(adjusted) == mergeinfo.String(value) {
					return database.SetProperty(ctx, targetPath, props.Mergeinfo, nil, false)
				}
			}
		}
	}
	text := mergeinfo.String(value)
	if text == "" {
		return database.SetProperty(ctx, targetPath, props.Mergeinfo, nil, false)
	}
	return database.SetProperty(ctx, targetPath, props.Mergeinfo, []byte(text), false)
}
