package client

import (
	"context"
	"fmt"

	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/wc"
)

type ConflictDescription struct {
	Path               string
	Kind               svn.NodeKind
	Text               bool
	Property           bool
	Tree               bool
	BasePath           string
	MinePath           string
	TheirsPath         string
	PropertyRejectPath string
	LocalChange        string
	IncomingChange     string
}

type ConflictResolver func(context.Context, ConflictDescription) (wc.ConflictChoice, error)

func (client *Client) DescribeConflict(ctx context.Context, targetPath string) (*ConflictDescription, error) {
	database, err := wc.Open(ctx, targetPath, wc.Options{})
	if err != nil {
		return nil, err
	}
	defer database.Close()
	info, err := database.Info(ctx, targetPath)
	if err != nil {
		return nil, err
	}
	if info.Conflict == nil {
		return nil, nil
	}
	description := conflictDescription(info)
	return &description, nil
}

func (client *Client) DescribeConflicts(ctx context.Context, targetPath string, depth svn.Depth) ([]ConflictDescription, error) {
	database, err := wc.Open(ctx, targetPath, wc.Options{})
	if err != nil {
		return nil, err
	}
	defer database.Close()
	var result []ConflictDescription
	err = database.Status(ctx, targetPath, wc.StatusOptions{Depth: depth}, func(status *wc.Status) error {
		if !status.Conflicted && !status.TreeConflicted {
			return nil
		}
		info, err := database.Info(ctx, status.Path)
		if err != nil {
			return err
		}
		if info.Conflict != nil {
			result = append(result, conflictDescription(info))
		}
		return nil
	})
	return result, err
}

func (client *Client) ResolveConflict(ctx context.Context, targetPath string, resolver ConflictResolver) error {
	if resolver == nil {
		return fmt.Errorf("%w: nil conflict resolver", svn.ErrIncorrectParams)
	}
	description, err := client.DescribeConflict(ctx, targetPath)
	if err != nil || description == nil {
		return err
	}
	choice, err := resolver(ctx, *description)
	if err != nil {
		return err
	}
	if choice == wc.ConflictPostpone {
		return nil
	}
	return client.ResolveWithChoice(ctx, targetPath, choice)
}

func (client *Client) ResolveConflicts(ctx context.Context, targetPath string, depth svn.Depth, resolver ConflictResolver) error {
	if resolver == nil {
		return fmt.Errorf("%w: nil conflict resolver", svn.ErrIncorrectParams)
	}
	descriptions, err := client.DescribeConflicts(ctx, targetPath, depth)
	if err != nil {
		return err
	}
	for _, description := range descriptions {
		choice, err := resolver(ctx, description)
		if err != nil {
			return err
		}
		if choice == wc.ConflictPostpone {
			continue
		}
		if err := client.ResolveWithChoice(ctx, description.Path, choice); err != nil {
			return err
		}
	}
	return nil
}

func conflictDescription(info *wc.Info) ConflictDescription {
	conflict := info.Conflict
	return ConflictDescription{
		Path: info.Path, Kind: info.Kind, Text: conflict.Text, Property: conflict.Property, Tree: conflict.Tree,
		BasePath: conflict.OldPath, MinePath: conflict.WorkingPath, TheirsPath: conflict.NewPath,
		PropertyRejectPath: conflict.PropertyPath, LocalChange: conflict.TreeReason, IncomingChange: conflict.TreeAction,
	}
}
