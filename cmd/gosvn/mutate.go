package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/otuschhoff/go-svn/client"
	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/wc"
)

func (cmd *command) checkout(ctx context.Context, args []string) error {
	flags := newFlagSet("checkout", cmd.stderr)
	revision := flags.String("revision", "", "revision")
	flags.StringVar(revision, "r", "", "revision")
	depthValue := flags.String("depth", "infinity", "checkout depth")
	ignoreExternals := flags.Bool("ignore-externals", false, "ignore externals")
	force := flags.Bool("force", false, "force obstructions")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() < 1 || flags.NArg() > 2 {
		return fmt.Errorf("checkout requires a URL and optional destination")
	}
	destination := filepath.Base(strings.TrimSuffix(flags.Arg(0), "/"))
	if flags.NArg() == 2 {
		destination = flags.Arg(1)
	}
	rev, err := parseRevnum(*revision)
	if err != nil {
		return err
	}
	depth, err := svn.ParseDepth(*depthValue)
	if err != nil {
		return err
	}
	rev, err = cmd.client.Checkout(ctx, flags.Arg(0), destination, rev, wc.UpdateOptions{Depth: depth, IgnoreExternals: *ignoreExternals, Force: *force})
	if err == nil {
		fmt.Fprintf(cmd.stdout, "Checked out revision %d.\n", rev)
	}
	return err
}

func (cmd *command) update(ctx context.Context, args []string) error {
	flags := newFlagSet("update", cmd.stderr)
	revision := flags.String("revision", "", "revision")
	flags.StringVar(revision, "r", "", "revision")
	depthValue := flags.String("depth", "unknown", "update depth")
	setDepthValue := flags.String("set-depth", "", "sticky depth")
	ignoreExternals := flags.Bool("ignore-externals", false, "ignore externals")
	force := flags.Bool("force", false, "force obstructions")
	parents := flags.Bool("parents", false, "create parents")
	acceptValue := flags.String("accept", "postpone", "conflict choice")
	if err := flags.Parse(args); err != nil {
		return err
	}
	targets := defaultTargets(flags.Args())
	rev, err := parseRevnum(*revision)
	if err != nil {
		return err
	}
	options, err := updateOptions(*depthValue, *setDepthValue, *acceptValue)
	if err != nil {
		return err
	}
	options.IgnoreExternals, options.Force, options.Parents = *ignoreExternals, *force, *parents
	for _, target := range targets {
		updated, err := cmd.client.Update(ctx, target, rev, options)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.stdout, "Updated to revision %d.\n", updated)
	}
	return nil
}

func (cmd *command) switchWorkingCopy(ctx context.Context, args []string) error {
	flags := newFlagSet("switch", cmd.stderr)
	revision := flags.String("revision", "", "revision")
	flags.StringVar(revision, "r", "", "revision")
	depthValue := flags.String("depth", "unknown", "update depth")
	ignoreExternals := flags.Bool("ignore-externals", false, "ignore externals")
	force := flags.Bool("force", false, "force obstructions")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() < 1 || flags.NArg() > 2 {
		return fmt.Errorf("switch requires a URL and optional working-copy path")
	}
	target := "."
	if flags.NArg() == 2 {
		target = flags.Arg(1)
	}
	rev, err := parseRevnum(*revision)
	if err != nil {
		return err
	}
	depth, err := svn.ParseDepth(*depthValue)
	if err != nil {
		return err
	}
	updated, err := cmd.client.Switch(ctx, target, flags.Arg(0), rev, wc.UpdateOptions{Depth: depth, IgnoreExternals: *ignoreExternals, Force: *force})
	if err == nil {
		fmt.Fprintf(cmd.stdout, "Updated to revision %d.\n", updated)
	}
	return err
}

func (cmd *command) commit(ctx context.Context, args []string) error {
	flags := newFlagSet("commit", cmd.stderr)
	message := flags.String("message", "", "log message")
	flags.StringVar(message, "m", "", "log message")
	messageFile := flags.String("file", "", "read log message from file")
	flags.StringVar(messageFile, "F", "", "read log message from file")
	depthValue := flags.String("depth", "infinity", "commit depth")
	keepLocks := flags.Bool("no-unlock", false, "keep locks")
	includeExternals := flags.Bool("include-externals", false, "include externals")
	changelists := stringListFlag{}
	flags.Var(&changelists, "changelist", "operate only on changelist")
	if err := flags.Parse(args); err != nil {
		return err
	}
	logMessage, err := cmd.logMessage(*message, *messageFile)
	if err != nil {
		return err
	}
	depth, err := svn.ParseDepth(*depthValue)
	if err != nil {
		return err
	}
	for _, target := range defaultTargets(flags.Args()) {
		info, err := cmd.client.Commit(ctx, target, wc.CommitOptions{RevisionProperties: svn.Props{props.Log: []byte(logMessage)}, Depth: depth, KeepLocks: *keepLocks, IncludeExternals: *includeExternals, Changelists: changelists})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.stdout, "Committed revision %d.\n", info.Revision)
	}
	return nil
}

func (cmd *command) add(ctx context.Context, args []string) error {
	flags := newFlagSet("add", cmd.stderr)
	force := flags.Bool("force", false, "force")
	parents := flags.Bool("parents", false, "create parents")
	noIgnore := flags.Bool("no-ignore", false, "disregard ignores")
	depthValue := flags.String("depth", "infinity", "operation depth")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return fmt.Errorf("add requires a target")
	}
	depth, err := svn.ParseDepth(*depthValue)
	if err != nil {
		return err
	}
	for _, target := range flags.Args() {
		if err := cmd.client.Add(ctx, target, wc.AddOptions{Depth: depth, Force: *force, Parents: *parents, NoIgnore: *noIgnore}); err != nil {
			return err
		}
	}
	return nil
}

func (cmd *command) delete(ctx context.Context, args []string) error {
	flags := newFlagSet("delete", cmd.stderr)
	force := flags.Bool("force", false, "force")
	keepLocal := flags.Bool("keep-local", false, "keep local files")
	message := flags.String("message", "", "log message")
	flags.StringVar(message, "m", "", "log message")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return fmt.Errorf("delete requires a target")
	}
	if allURLs(flags.Args()) {
		logMessage, err := cmd.logMessage(*message, "")
		if err != nil {
			return err
		}
		root, paths, revision, err := cmd.repositoryTargets(ctx, flags.Args())
		if err != nil {
			return err
		}
		info, err := cmd.client.DeleteURL(ctx, root, paths, revision, svn.Props{props.Log: []byte(logMessage)})
		if err == nil {
			fmt.Fprintf(cmd.stdout, "Committed revision %d.\n", info.Revision)
		}
		return err
	}
	for _, target := range flags.Args() {
		if err := cmd.client.Delete(ctx, target, wc.DeleteOptions{KeepLocal: *keepLocal, Force: *force}); err != nil {
			return err
		}
	}
	return nil
}

func (cmd *command) copy(ctx context.Context, args []string) error {
	flags := newFlagSet("copy", cmd.stderr)
	revisionValue := flags.String("revision", "", "copy revision")
	flags.StringVar(revisionValue, "r", "", "copy revision")
	message := flags.String("message", "", "log message")
	flags.StringVar(message, "m", "", "log message")
	pinExternals := flags.Bool("pin-externals", false, "pin externals")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return fmt.Errorf("copy requires source and destination")
	}
	if isURL(flags.Arg(0)) && isURL(flags.Arg(1)) {
		logMessage, err := cmd.logMessage(*message, "")
		if err != nil {
			return err
		}
		revision, err := parseRevnum(*revisionValue)
		if err != nil {
			return err
		}
		info, err := cmd.client.Info(ctx, flags.Arg(0), client.InfoOptions{})
		if err != nil {
			return err
		}
		if !revision.IsValid() {
			revision = info.Revision
		}
		root, destination, _, err := cmd.repositoryTarget(ctx, flags.Arg(1))
		if err != nil {
			return err
		}
		commit, err := cmd.client.CopyURLWithOptions(ctx, root, flags.Arg(0), destination, revision, info.Kind, client.CopyURLOptions{RevisionProperties: svn.Props{props.Log: []byte(logMessage)}, PinExternals: *pinExternals})
		if err == nil {
			fmt.Fprintf(cmd.stdout, "Committed revision %d.\n", commit.Revision)
		}
		return err
	}
	return cmd.client.Copy(ctx, flags.Arg(0), flags.Arg(1))
}

func (cmd *command) move(ctx context.Context, args []string) error {
	flags := newFlagSet("move", cmd.stderr)
	force := flags.Bool("force", false, "force")
	message := flags.String("message", "", "log message")
	flags.StringVar(message, "m", "", "log message")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return fmt.Errorf("move requires source and destination")
	}
	if isURL(flags.Arg(0)) && isURL(flags.Arg(1)) {
		logMessage, err := cmd.logMessage(*message, "")
		if err != nil {
			return err
		}
		info, err := cmd.client.Info(ctx, flags.Arg(0), client.InfoOptions{})
		if err != nil {
			return err
		}
		root, destination, _, err := cmd.repositoryTarget(ctx, flags.Arg(1))
		if err != nil {
			return err
		}
		commit, err := cmd.client.MoveURL(ctx, root, flags.Arg(0), destination, info.Revision, info.Kind, svn.Props{props.Log: []byte(logMessage)})
		if err == nil {
			fmt.Fprintf(cmd.stdout, "Committed revision %d.\n", commit.Revision)
		}
		return err
	}
	return cmd.client.Move(ctx, flags.Arg(0), flags.Arg(1), *force)
}

func (cmd *command) mkdir(ctx context.Context, args []string) error {
	flags := newFlagSet("mkdir", cmd.stderr)
	parents := flags.Bool("parents", false, "create parents")
	message := flags.String("message", "", "log message")
	flags.StringVar(message, "m", "", "log message")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return fmt.Errorf("mkdir requires a target")
	}
	if allURLs(flags.Args()) {
		logMessage, err := cmd.logMessage(*message, "")
		if err != nil {
			return err
		}
		root, paths, _, err := cmd.repositoryTargets(ctx, flags.Args())
		if err != nil {
			return err
		}
		info, err := cmd.client.MkdirURL(ctx, root, paths, *parents, svn.Props{props.Log: []byte(logMessage)})
		if err == nil {
			fmt.Fprintf(cmd.stdout, "Committed revision %d.\n", info.Revision)
		}
		return err
	}
	for _, target := range flags.Args() {
		if err := cmd.client.Mkdir(ctx, target, *parents); err != nil {
			return err
		}
	}
	return nil
}

func (cmd *command) propset(ctx context.Context, args []string, remove bool) error {
	flags := newFlagSet("propset", cmd.stderr)
	force := flags.Bool("force", false, "force")
	file := flags.String("file", "", "read value from file")
	flags.StringVar(file, "F", "", "read value from file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	minimum := 2
	if !remove {
		minimum = 3
	}
	if flags.NArg() < minimum {
		return fmt.Errorf("property name, value, and target required")
	}
	name := flags.Arg(0)
	var value []byte
	var targets []string
	if remove {
		targets = flags.Args()[1:]
	} else {
		value = []byte(flags.Arg(1))
		targets = flags.Args()[2:]
		if *file != "" {
			var err error
			value, err = os.ReadFile(*file)
			if err != nil {
				return err
			}
		}
	}
	for _, target := range targets {
		if err := cmd.client.SetProperty(ctx, target, name, value, *force); err != nil {
			return err
		}
	}
	return nil
}

func (cmd *command) revert(ctx context.Context, args []string) error {
	flags := newFlagSet("revert", cmd.stderr)
	depthValue := flags.String("depth", "empty", "operation depth")
	removeAdded := flags.Bool("remove-added", false, "remove added paths")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return fmt.Errorf("revert requires a target")
	}
	depth, err := svn.ParseDepth(*depthValue)
	if err != nil {
		return err
	}
	for _, target := range flags.Args() {
		if err := cmd.client.Revert(ctx, target, wc.RevertOptions{Depth: depth, RemoveAdded: *removeAdded}); err != nil {
			return err
		}
	}
	return nil
}

func (cmd *command) resolve(ctx context.Context, args []string) error {
	flags := newFlagSet("resolve", cmd.stderr)
	acceptValue := flags.String("accept", "working", "conflict choice")
	depthValue := flags.String("depth", "empty", "operation depth")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return fmt.Errorf("resolve requires a target")
	}
	choice, err := parseAccept(*acceptValue)
	if err != nil {
		return err
	}
	depth, err := svn.ParseDepth(*depthValue)
	if err != nil {
		return err
	}
	for _, target := range flags.Args() {
		if err := cmd.client.ResolveConflicts(ctx, target, depth, func(context.Context, client.ConflictDescription) (wc.ConflictChoice, error) { return choice, nil }); err != nil {
			return err
		}
	}
	return nil
}

func (cmd *command) cleanup(ctx context.Context, args []string) error {
	flags := newFlagSet("cleanup", cmd.stderr)
	removeUnversioned := flags.Bool("remove-unversioned", false, "remove unversioned paths")
	removeIgnored := flags.Bool("remove-ignored", false, "remove ignored paths")
	includeExternals := flags.Bool("include-externals", false, "include externals")
	if err := flags.Parse(args); err != nil {
		return err
	}
	for _, target := range defaultTargets(flags.Args()) {
		if err := cmd.client.CleanupWithOptions(ctx, target, wc.CleanupOptions{RemoveUnversioned: *removeUnversioned, RemoveIgnored: *removeIgnored, VacuumPristines: true, IncludeExternals: *includeExternals}); err != nil {
			return err
		}
	}
	return nil
}

func (cmd *command) upgrade(ctx context.Context, args []string) error {
	flags := newFlagSet("upgrade", cmd.stderr)
	if err := flags.Parse(args); err != nil {
		return err
	}
	for _, target := range defaultTargets(flags.Args()) {
		if err := cmd.client.Upgrade(ctx, target); err != nil {
			return err
		}
	}
	return nil
}

func (cmd *command) lock(ctx context.Context, args []string) error {
	flags := newFlagSet("lock", cmd.stderr)
	force := flags.Bool("force", false, "steal lock")
	message := flags.String("message", "", "lock comment")
	flags.StringVar(message, "m", "", "lock comment")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return fmt.Errorf("lock requires a target")
	}
	for _, target := range flags.Args() {
		if _, err := cmd.client.Lock(ctx, target, client.LockOptions{Comment: *message, Force: *force}); err != nil {
			return err
		}
	}
	return nil
}

func (cmd *command) unlock(ctx context.Context, args []string) error {
	flags := newFlagSet("unlock", cmd.stderr)
	force := flags.Bool("force", false, "break lock")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return fmt.Errorf("unlock requires a target")
	}
	for _, target := range flags.Args() {
		if err := cmd.client.Unlock(ctx, target, *force); err != nil {
			return err
		}
	}
	return nil
}

func (cmd *command) export(ctx context.Context, args []string) error {
	flags := newFlagSet("export", cmd.stderr)
	revision := flags.String("revision", "", "revision")
	flags.StringVar(revision, "r", "", "revision")
	force := flags.Bool("force", false, "overwrite")
	ignoreExternals := flags.Bool("ignore-externals", false, "ignore externals")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return fmt.Errorf("export requires source and destination")
	}
	parsed, err := parseRevision(*revision)
	if err != nil {
		return err
	}
	rev, err := cmd.client.Export(ctx, flags.Arg(0), flags.Arg(1), client.ExportOptions{InfoOptions: client.InfoOptions{Revision: parsed}, Force: *force, IgnoreExternals: *ignoreExternals})
	if err == nil {
		fmt.Fprintf(cmd.stdout, "Exported revision %d.\n", rev)
	}
	return err
}

func (cmd *command) importPath(ctx context.Context, args []string) error {
	flags := newFlagSet("import", cmd.stderr)
	message := flags.String("message", "", "log message")
	flags.StringVar(message, "m", "", "log message")
	parents := flags.Bool("parents", false, "create parents")
	noIgnore := flags.Bool("no-ignore", false, "disregard ignores")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		return fmt.Errorf("import requires local source and repository URL")
	}
	logMessage, err := cmd.logMessage(*message, "")
	if err != nil {
		return err
	}
	root, destination, _, err := cmd.repositoryTarget(ctx, flags.Arg(1))
	if err != nil {
		return err
	}
	info, err := cmd.client.Import(ctx, flags.Arg(0), root, destination, client.ImportOptions{RevisionProperties: svn.Props{props.Log: []byte(logMessage)}, Depth: svn.DepthInfinity, Parents: *parents, NoIgnore: *noIgnore})
	if err == nil {
		fmt.Fprintf(cmd.stdout, "Committed revision %d.\n", info.Revision)
	}
	return err
}

func (cmd *command) merge(ctx context.Context, args []string) error {
	flags := newFlagSet("merge", cmd.stderr)
	revision := flags.String("revision", "", "revision range")
	flags.StringVar(revision, "r", "", "revision range")
	changes := stringListFlag{}
	flags.Var(&changes, "change", "change number")
	flags.Var(&changes, "c", "change number")
	recordOnly := flags.Bool("record-only", false, "record mergeinfo only")
	ignoreAncestry := flags.Bool("ignore-ancestry", false, "ignore ancestry")
	dryRun := flags.Bool("dry-run", false, "do not change working copy")
	force := flags.Bool("force", false, "force")
	acceptValue := flags.String("accept", "postpone", "conflict choice")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() < 1 || flags.NArg() > 3 {
		return fmt.Errorf("merge requires source and optional target")
	}
	if *revision != "" && len(changes) != 0 {
		return fmt.Errorf("--revision and --change are mutually exclusive")
	}
	ranges, err := parseRevisionRanges(*revision)
	if err != nil {
		return err
	}
	if len(changes) != 0 {
		ranges, err = parseChangeRanges(changes)
		if err != nil {
			return err
		}
	}
	accept, err := parseAccept(*acceptValue)
	if err != nil {
		return err
	}
	options := client.MergeOptions{Ranges: ranges, Depth: svn.DepthInfinity, RecordOnly: *recordOnly, IgnoreAncestry: *ignoreAncestry, DryRun: *dryRun, Force: *force, Accept: accept}
	if flags.NArg() == 3 {
		left := client.DiffTarget{Target: flags.Arg(0)}
		right := client.DiffTarget{Target: flags.Arg(1)}
		return cmd.client.MergeTwoSources(ctx, left, right, flags.Arg(2), options)
	}
	target := "."
	if flags.NArg() == 2 {
		target = flags.Arg(1)
	}
	return cmd.client.Merge(ctx, flags.Arg(0), target, options)
}

func (cmd *command) changelist(ctx context.Context, args []string) error {
	flags := newFlagSet("changelist", cmd.stderr)
	remove := flags.Bool("remove", false, "remove changelist")
	if err := flags.Parse(args); err != nil {
		return err
	}
	minimum := 2
	if *remove {
		minimum = 1
	}
	if flags.NArg() < minimum {
		return fmt.Errorf("changelist requires a target")
	}
	name := ""
	targets := flags.Args()
	if !*remove {
		name = flags.Arg(0)
		targets = flags.Args()[1:]
	}
	for _, target := range targets {
		if err := cmd.client.SetChangelist(ctx, target, name); err != nil {
			return err
		}
	}
	return nil
}

func parseChangeRanges(values []string) ([]svn.RevisionRange, error) {
	var ranges []svn.RevisionRange
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			number, err := strconv.ParseInt(strings.TrimSpace(item), 10, 64)
			if err != nil || number == 0 {
				return nil, fmt.Errorf("invalid change number %q", item)
			}
			if number > 0 {
				ranges = append(ranges, svn.RevisionRange{Start: svn.Revision{Kind: svn.RevisionNumber, Number: svn.Revnum(number - 1)}, End: svn.Revision{Kind: svn.RevisionNumber, Number: svn.Revnum(number)}})
			} else {
				number = -number
				ranges = append(ranges, svn.RevisionRange{Start: svn.Revision{Kind: svn.RevisionNumber, Number: svn.Revnum(number)}, End: svn.Revision{Kind: svn.RevisionNumber, Number: svn.Revnum(number - 1)}})
			}
		}
	}
	return ranges, nil
}

func updateOptions(depthValue, setDepthValue, acceptValue string) (wc.UpdateOptions, error) {
	depth, err := svn.ParseDepth(depthValue)
	if err != nil {
		return wc.UpdateOptions{}, err
	}
	accept, err := parseAccept(acceptValue)
	if err != nil {
		return wc.UpdateOptions{}, err
	}
	options := wc.UpdateOptions{Depth: depth, Accept: accept}
	if setDepthValue != "" {
		setDepth, err := svn.ParseDepth(setDepthValue)
		if err != nil {
			return wc.UpdateOptions{}, err
		}
		options.SetDepth = &setDepth
	}
	return options, nil
}

func parseAccept(value string) (wc.ConflictChoice, error) {
	switch value {
	case "postpone":
		return wc.ConflictPostpone, nil
	case "working":
		return wc.ConflictWorking, nil
	case "base":
		return wc.ConflictBase, nil
	case "mine-full", "mine":
		return wc.ConflictMine, nil
	case "theirs-full", "theirs":
		return wc.ConflictTheirs, nil
	case "mine-conflict":
		return wc.ConflictMineConflict, nil
	case "theirs-conflict":
		return wc.ConflictTheirsConflict, nil
	default:
		return wc.ConflictPostpone, fmt.Errorf("unknown conflict choice %q", value)
	}
}

func parseRevnum(value string) (svn.Revnum, error) {
	if value == "" || strings.EqualFold(value, "HEAD") {
		return svn.InvalidRevnum, nil
	}
	revision, err := svn.ParseRevision(value)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	if revision.Kind != svn.RevisionNumber {
		return svn.InvalidRevnum, fmt.Errorf("revision must be numeric or HEAD")
	}
	return revision.Number, nil
}

func defaultTargets(values []string) []string {
	if len(values) == 0 {
		return []string{"."}
	}
	return values
}

func isURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != ""
}

func allURLs(values []string) bool {
	for _, value := range values {
		if !isURL(value) {
			return false
		}
	}
	return true
}

func (cmd *command) repositoryTarget(ctx context.Context, value string) (string, string, svn.Revnum, error) {
	session, _, err := ra.Open(ctx, value, cmd.client.Callbacks)
	if err != nil {
		return "", "", svn.InvalidRevnum, err
	}
	defer session.Close()
	root, err := session.RepositoryRoot(ctx)
	if err != nil {
		return "", "", svn.InvalidRevnum, err
	}
	revision, err := session.LatestRevision(ctx)
	if err != nil {
		return "", "", svn.InvalidRevnum, err
	}
	parsedRoot, err := url.Parse(root)
	if err != nil {
		return "", "", svn.InvalidRevnum, err
	}
	parsedValue, err := url.Parse(value)
	if err != nil {
		return "", "", svn.InvalidRevnum, err
	}
	relative := strings.TrimPrefix(strings.TrimPrefix(path.Clean(parsedValue.Path), strings.TrimSuffix(parsedRoot.Path, "/")), "/")
	return root, relative, revision, nil
}

func (cmd *command) repositoryTargets(ctx context.Context, values []string) (string, []string, svn.Revnum, error) {
	var root string
	var revision svn.Revnum
	paths := make([]string, 0, len(values))
	for _, value := range values {
		itemRoot, itemPath, itemRevision, err := cmd.repositoryTarget(ctx, value)
		if err != nil {
			return "", nil, svn.InvalidRevnum, err
		}
		if root != "" && root != itemRoot {
			return "", nil, svn.InvalidRevnum, fmt.Errorf("targets are in different repositories")
		}
		root, revision = itemRoot, itemRevision
		paths = append(paths, itemPath)
	}
	return root, paths, revision, nil
}

func (cmd *command) logMessage(value, filename string) (string, error) {
	if value != "" && filename != "" {
		return "", fmt.Errorf("--message and --file are mutually exclusive")
	}
	if filename != "" {
		contents, err := os.ReadFile(filename)
		return string(contents), err
	}
	if value != "" {
		return value, nil
	}
	if cmd.global.nonInteractive {
		return "", fmt.Errorf("log message required in non-interactive mode")
	}
	editor := firstNonempty(os.Getenv("SVN_EDITOR"), os.Getenv("VISUAL"), os.Getenv("EDITOR"))
	if editor == "" {
		return "", fmt.Errorf("no editor configured; use --message or --file")
	}
	temporary, err := os.CreateTemp("", "gosvn-commit-*.tmp")
	if err != nil {
		return "", err
	}
	name := temporary.Name()
	if err := temporary.Close(); err != nil {
		return "", err
	}
	defer os.Remove(name)
	parts := strings.Fields(editor)
	process := exec.Command(parts[0], append(parts[1:], name)...)
	process.Stdin, process.Stdout, process.Stderr = cmd.stdin, cmd.stdout, cmd.stderr
	if err := process.Run(); err != nil {
		return "", err
	}
	contents, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(contents)) == "" {
		return "", fmt.Errorf("empty log message")
	}
	return string(contents), nil
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type stringListFlag []string

func (values *stringListFlag) String() string { return strings.Join(*values, ",") }
func (values *stringListFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}
