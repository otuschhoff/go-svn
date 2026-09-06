package wc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/svn"
)

func (database *Database) OpenTextBase(ctx context.Context, targetPath string) (io.ReadCloser, error) {
	info, err := database.Info(ctx, targetPath)
	if err != nil {
		return nil, err
	}
	if info.Kind != svn.NodeFile && info.Kind != svn.NodeSymlink {
		return nil, fmt.Errorf("%w: %s", svn.ErrWCNotFile, targetPath)
	}
	if info.Checksum == nil {
		return nil, fmt.Errorf("%w: %s has no pristine text", svn.ErrWCPathNotFound, targetPath)
	}
	return database.OpenPristine(ctx, *info.Checksum)
}

func (database *Database) OpenTranslatedTextBase(ctx context.Context, targetPath string) (io.ReadCloser, error) {
	info, err := database.Info(ctx, targetPath)
	if err != nil {
		return nil, err
	}
	base, err := database.OpenTextBase(ctx, targetPath)
	if err != nil {
		return nil, err
	}
	if len(info.BaseProperties[props.Special]) != 0 {
		data, readErr := io.ReadAll(base)
		base.Close()
		if readErr != nil {
			return nil, readErr
		}
		target, decodeErr := props.DecodeSpecial(data)
		if decodeErr != nil {
			return nil, decodeErr
		}
		return io.NopCloser(bytes.NewReader([]byte(target))), nil
	}
	reader, writer := io.Pipe()
	go func() {
		defer base.Close()
		keywords := props.ParseKeywords(string(info.BaseProperties[props.Keywords]), props.KeywordContext{
			Author: info.ChangedAuthor, Basename: filepath.Base(info.Path), Date: info.ChangedDate,
			Path: info.RepositoryPath, Revision: info.ChangedRevision, RootURL: info.RepositoryRoot, URL: info.URL,
		})
		err := props.Translate(base, writer, string(info.BaseProperties[props.EOLStyle]), keywords, true, true)
		_ = writer.CloseWithError(err)
	}()
	return reader, nil
}
