package wc

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/skel"
)

type PropertyLayer uint8

const (
	PropertiesWorking PropertyLayer = iota
	PropertiesBase
)

type InheritedProperties struct {
	RepositoryPath string
	Properties     svn.Props
}

func (database *Database) PropList(ctx context.Context, targetPath string, layer PropertyLayer) (svn.Props, error) {
	info, err := database.Info(ctx, targetPath)
	if err != nil {
		return nil, err
	}
	if layer == PropertiesBase {
		return info.BaseProperties.Clone(), nil
	}
	return info.WorkingProperties.Clone(), nil
}

func (database *Database) PropGet(ctx context.Context, targetPath, name string, layer PropertyLayer) ([]byte, bool, error) {
	properties, err := database.PropList(ctx, targetPath, layer)
	if err != nil {
		return nil, false, err
	}
	value, ok := properties[name]
	return append([]byte(nil), value...), ok, nil
}

func (database *Database) InheritedPropList(ctx context.Context, targetPath string) ([]InheritedProperties, error) {
	relpath, err := database.localRelpath(targetPath)
	if err != nil {
		return nil, err
	}
	database.mu.Lock()
	if cached, ok := database.inherited[relpath]; ok {
		result := cloneInherited(cached)
		database.mu.Unlock()
		return result, nil
	}
	database.mu.Unlock()
	var data []byte
	err = database.sql.QueryRowContext(ctx, `SELECT inherited_props FROM NODES_CURRENT WHERE wc_id = ? AND local_relpath = ?`, database.wcID, relpath).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: %s", svn.ErrWCPathNotFound, targetPath)
	}
	if err != nil {
		return nil, err
	}
	result, err := decodeInheritedProperties(data)
	if err != nil {
		return nil, err
	}
	database.mu.Lock()
	database.inherited[relpath] = cloneInherited(result)
	database.mu.Unlock()
	return result, nil
}

func decodeInheritedProperties(data []byte) ([]InheritedProperties, error) {
	if len(data) == 0 {
		return nil, nil
	}
	parsed, err := skel.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: inherited properties skeleton: %v", svn.ErrWCCorrupt, err)
	}
	if !parsed.IsList() {
		return nil, fmt.Errorf("%w: inherited properties are not a list", svn.ErrWCCorrupt)
	}
	if len(parsed.Children)%2 != 0 {
		return nil, fmt.Errorf("%w: invalid inherited property list", svn.ErrWCCorrupt)
	}
	result := make([]InheritedProperties, 0, len(parsed.Children)/2)
	for index := 0; index < len(parsed.Children); index += 2 {
		if !parsed.Children[index].IsAtom() {
			return nil, fmt.Errorf("%w: invalid inherited property path", svn.ErrWCCorrupt)
		}
		properties, err := skel.ProplistToProps(parsed.Children[index+1])
		if err != nil {
			return nil, fmt.Errorf("%w: inherited property entry: %v", svn.ErrWCCorrupt, err)
		}
		result = append(result, InheritedProperties{RepositoryPath: string(parsed.Children[index].Atom), Properties: properties})
	}
	return result, nil
}

func cloneInherited(source []InheritedProperties) []InheritedProperties {
	result := make([]InheritedProperties, len(source))
	for index, item := range source {
		result[index] = InheritedProperties{RepositoryPath: item.RepositoryPath, Properties: item.Properties.Clone()}
	}
	return result
}

func (database *Database) propWalk(ctx context.Context, targetPath string, depth svn.Depth, layer PropertyLayer, callback func(string, svn.Props) error) error {
	relpath, err := database.localRelpath(targetPath)
	if err != nil {
		return err
	}
	paths, err := database.versionedPaths(ctx, relpath, depth)
	if err != nil {
		return err
	}
	for _, name := range paths {
		properties, err := database.PropList(ctx, filepath.Join(database.wcRoot, filepath.FromSlash(name)), layer)
		if err != nil {
			return err
		}
		if err := callback(name, properties); err != nil {
			return err
		}
	}
	return nil
}

func (database *Database) WalkProperties(ctx context.Context, targetPath string, depth svn.Depth, layer PropertyLayer, callback func(string, svn.Props) error) error {
	return database.propWalk(ctx, targetPath, depth, layer, callback)
}
