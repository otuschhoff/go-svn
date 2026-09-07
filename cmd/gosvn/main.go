package main

import (
	"context"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/otuschhoff/go-svn/client"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/wc"
)

const version = "0.1.0"

type command struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	client *client.Client
	global globalOptions
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	options, remaining, err := parseGlobalOptions(args)
	if err != nil {
		fmt.Fprintf(stderr, "gosvn: E%06d: %v\n", errorCode(err), err)
		return 1
	}
	callbacks, err := makeCallbacks(options, stdin, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "gosvn: E%06d: %v\n", errorCode(err), err)
		return 1
	}
	cmd := &command{stdin: stdin, stdout: stdout, stderr: stderr, global: options}
	callbacks.Notify = cmd.notify
	cmd.client = client.New(callbacks)
	if err := cmd.execute(ctx, remaining); err != nil {
		fmt.Fprintf(stderr, "gosvn: E%06d: %v\n", errorCode(err), err)
		return 1
	}
	return 0
}

func (cmd *command) execute(ctx context.Context, args []string) error {
	if len(args) == 0 {
		cmd.usage()
		return nil
	}
	switch args[0] {
	case "help", "--help", "-h":
		if len(args) > 1 {
			return cmd.commandHelp(args[1])
		}
		cmd.usage()
		return nil
	case "version", "--version":
		fmt.Fprintf(cmd.stdout, "gosvn, version %s\n", version)
		return nil
	case "info":
		return cmd.info(ctx, args[1:])
	case "cat":
		return cmd.cat(ctx, args[1:])
	case "list", "ls":
		return cmd.list(ctx, args[1:])
	case "status", "stat", "st":
		return cmd.status(ctx, args[1:])
	case "log":
		return cmd.log(ctx, args[1:])
	case "blame", "praise", "annotate":
		return cmd.blame(ctx, args[1:])
	case "diff", "di":
		return cmd.diff(ctx, args[1:])
	case "proplist", "plist", "pl":
		return cmd.proplist(ctx, args[1:])
	case "propget", "pget", "pg":
		return cmd.propget(ctx, args[1:])
	case "checkout", "co":
		return cmd.checkout(ctx, args[1:])
	case "update", "up":
		return cmd.update(ctx, args[1:])
	case "switch", "sw":
		return cmd.switchWorkingCopy(ctx, args[1:])
	case "commit", "ci":
		return cmd.commit(ctx, args[1:])
	case "add":
		return cmd.add(ctx, args[1:])
	case "delete", "del", "remove", "rm":
		return cmd.delete(ctx, args[1:])
	case "copy", "cp":
		return cmd.copy(ctx, args[1:])
	case "move", "mv", "rename", "ren":
		return cmd.move(ctx, args[1:])
	case "mkdir":
		return cmd.mkdir(ctx, args[1:])
	case "propset", "pset", "ps":
		return cmd.propset(ctx, args[1:], false)
	case "propdel", "pdel", "pd":
		return cmd.propset(ctx, args[1:], true)
	case "revert":
		return cmd.revert(ctx, args[1:])
	case "resolve", "resolved":
		return cmd.resolve(ctx, args[1:])
	case "cleanup":
		return cmd.cleanup(ctx, args[1:])
	case "upgrade":
		return cmd.upgrade(ctx, args[1:])
	case "lock":
		return cmd.lock(ctx, args[1:])
	case "unlock":
		return cmd.unlock(ctx, args[1:])
	case "export":
		return cmd.export(ctx, args[1:])
	case "import":
		return cmd.importPath(ctx, args[1:])
	case "merge":
		return cmd.merge(ctx, args[1:])
	case "changelist", "cl":
		return cmd.changelist(ctx, args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func (cmd *command) usage() {
	fmt.Fprintln(cmd.stdout, "usage: gosvn <subcommand> [options] [args]")
	fmt.Fprintln(cmd.stdout, "subcommands: add, blame, cat, changelist, checkout, cleanup, commit, copy, delete, diff, export, import, info, list, lock, log, merge, mkdir, move, propdel, propget, proplist, propset, resolve, revert, status, switch, unlock, update, upgrade, help, version")
}

func (cmd *command) commandHelp(name string) error {
	canonical := map[string]string{
		"co": "checkout", "up": "update", "sw": "switch", "ci": "commit",
		"del": "delete", "remove": "delete", "rm": "delete", "cp": "copy",
		"mv": "move", "rename": "move", "ren": "move", "pset": "propset", "ps": "propset",
		"pdel": "propdel", "pd": "propdel", "resolved": "resolve", "cl": "changelist",
		"ls": "list", "stat": "status", "st": "status", "praise": "blame", "annotate": "blame",
		"di": "diff", "plist": "proplist", "pl": "proplist", "pget": "propget", "pg": "propget",
	}
	if resolved := canonical[name]; resolved != "" {
		name = resolved
	}
	known := map[string]bool{
		"add": true, "blame": true, "cat": true, "changelist": true, "checkout": true,
		"cleanup": true, "commit": true, "copy": true, "delete": true, "diff": true,
		"export": true, "import": true, "info": true, "list": true, "lock": true,
		"log": true, "merge": true, "mkdir": true, "move": true, "propdel": true,
		"propget": true, "proplist": true, "propset": true, "resolve": true, "revert": true,
		"status": true, "switch": true, "unlock": true, "update": true, "upgrade": true,
	}
	if !known[name] {
		return fmt.Errorf("unknown subcommand %q", name)
	}
	fmt.Fprintf(cmd.stdout, "usage: gosvn %s [options] [args]\n", name)
	return nil
}

func (cmd *command) info(ctx context.Context, args []string) error {
	flags := newFlagSet("info", cmd.stderr)
	xmlOutput := flags.Bool("xml", false, "output in XML")
	revision := flags.String("revision", "", "operative revision")
	flags.StringVar(revision, "r", "", "operative revision")
	if err := flags.Parse(args); err != nil {
		return err
	}
	targets := flags.Args()
	if len(targets) == 0 {
		targets = []string{"."}
	}
	entries := make([]infoXMLEntry, 0, len(targets))
	for _, target := range targets {
		parsedRevision, err := parseRevision(*revision)
		if err != nil {
			return err
		}
		info, err := cmd.client.Info(ctx, target, client.InfoOptions{Revision: parsedRevision})
		if err != nil {
			return err
		}
		if *xmlOutput {
			entries = append(entries, makeInfoXMLEntry(info))
		} else {
			printInfo(cmd.stdout, info)
		}
	}
	if *xmlOutput {
		return writeXML(cmd.stdout, infoXML{Entries: entries})
	}
	return nil
}

func (cmd *command) cat(ctx context.Context, args []string) error {
	flags := newFlagSet("cat", cmd.stderr)
	revision := flags.String("revision", "", "operative revision")
	flags.StringVar(revision, "r", "", "operative revision")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return fmt.Errorf("cat requires at least one target")
	}
	parsedRevision, err := parseRevision(*revision)
	if err != nil {
		return err
	}
	for _, target := range flags.Args() {
		if err := cmd.client.Cat(ctx, target, cmd.stdout, client.CatOptions{InfoOptions: client.InfoOptions{Revision: parsedRevision}}); err != nil {
			return err
		}
	}
	return nil
}

func (cmd *command) list(ctx context.Context, args []string) error {
	flags := newFlagSet("list", cmd.stderr)
	xmlOutput := flags.Bool("xml", false, "output in XML")
	verbose := flags.Bool("verbose", false, "show detailed information")
	flags.BoolVar(verbose, "v", false, "show detailed information")
	recursive := flags.Bool("recursive", false, "descend recursively")
	flags.BoolVar(recursive, "R", false, "descend recursively")
	revision := flags.String("revision", "", "operative revision")
	flags.StringVar(revision, "r", "", "operative revision")
	if err := flags.Parse(args); err != nil {
		return err
	}
	targets := flags.Args()
	if len(targets) == 0 {
		targets = []string{"."}
	}
	parsedRevision, err := parseRevision(*revision)
	if err != nil {
		return err
	}
	depth := svn.DepthImmediates
	if *recursive {
		depth = svn.DepthInfinity
	}
	result := listXML{}
	for _, target := range targets {
		list := listXMLList{Path: target}
		err := cmd.client.List(ctx, target, client.ListOptions{InfoOptions: client.InfoOptions{Revision: parsedRevision}, Depth: depth}, func(entry client.ListEntry) error {
			if *xmlOutput {
				list.Entries = append(list.Entries, makeListXMLEntry(entry))
			} else if *verbose {
				fmt.Fprintf(cmd.stdout, "%7d %-8s %10d %s %s\n", entry.Entry.CreatedRev, entry.Entry.LastAuthor, entry.Entry.Size, formatHumanTime(entry.Entry.Time), displayListPath(entry))
			} else {
				fmt.Fprintln(cmd.stdout, displayListPath(entry))
			}
			return nil
		})
		if err != nil {
			return err
		}
		result.Lists = append(result.Lists, list)
	}
	if *xmlOutput {
		return writeXML(cmd.stdout, result)
	}
	return nil
}

func (cmd *command) status(ctx context.Context, args []string) error {
	flags := newFlagSet("status", cmd.stderr)
	xmlOutput := flags.Bool("xml", false, "output in XML")
	verbose := flags.Bool("verbose", false, "show revision information")
	flags.BoolVar(verbose, "v", false, "show revision information")
	quiet := flags.Bool("quiet", false, "show only locally modified items")
	flags.BoolVar(quiet, "q", false, "show only locally modified items")
	if err := flags.Parse(args); err != nil {
		return err
	}
	targets := flags.Args()
	if len(targets) == 0 {
		targets = []string{"."}
	}
	result := statusXML{}
	for _, target := range targets {
		database, err := wc.Open(ctx, target, wc.Options{})
		if err != nil {
			return err
		}
		var statuses []*wc.Status
		err = database.Status(ctx, target, wc.StatusOptions{Depth: svn.DepthInfinity, Verbose: *verbose}, func(status *wc.Status) error {
			copy := *status
			statuses = append(statuses, &copy)
			return nil
		})
		closeErr := database.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		sort.Slice(statuses, func(i, j int) bool { return statuses[i].Path < statuses[j].Path })
		targetXML := statusXMLTarget{Path: target}
		for _, status := range statuses {
			if *quiet && status.NodeStatus == wc.StatusUnversioned {
				continue
			}
			if *xmlOutput {
				targetXML.Entries = append(targetXML.Entries, makeStatusXMLEntry(status))
			} else if *verbose || status.NodeStatus != wc.StatusNormal || status.PropertyStatus != wc.StatusNormal {
				printStatus(cmd.stdout, status, *verbose)
			}
		}
		result.Targets = append(result.Targets, targetXML)
	}
	if *xmlOutput {
		return writeXML(cmd.stdout, result)
	}
	return nil
}

func newFlagSet(name string, output io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(output)
	return flags
}

func parseRevision(value string) (svn.Revision, error) {
	if value == "" {
		return svn.Revision{}, nil
	}
	return svn.ParseRevision(value)
}

func errorCode(err error) int {
	var svnError *svn.Error
	if errors.As(err, &svnError) {
		return int(svnError.Code)
	}
	return int(svn.ErrBase)
}

func writeXML(output io.Writer, value any) error {
	if _, err := io.WriteString(output, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"); err != nil {
		return err
	}
	encoder := xml.NewEncoder(output)
	encoder.Indent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	return encoder.Flush()
}

type infoXML struct {
	XMLName xml.Name       `xml:"info"`
	Entries []infoXMLEntry `xml:"entry"`
}

type infoXMLEntry struct {
	Kind       string        `xml:"kind,attr"`
	Path       string        `xml:"path,attr"`
	Revision   svn.Revnum    `xml:"revision,attr"`
	Size       *int64        `xml:"size,attr,omitempty"`
	URL        string        `xml:"url"`
	Relative   string        `xml:"relative-url"`
	Repository repositoryXML `xml:"repository"`
	Commit     commitXML     `xml:"commit"`
}

type repositoryXML struct {
	Root string `xml:"root"`
	UUID string `xml:"uuid"`
}

type commitXML struct {
	Revision svn.Revnum `xml:"revision,attr"`
	Author   string     `xml:"author,omitempty"`
	Date     string     `xml:"date,omitempty"`
}

func makeInfoXMLEntry(info *client.Info) infoXMLEntry {
	entryPath := info.Path
	if parsed, err := url.Parse(info.Path); err == nil && parsed.Scheme != "" {
		entryPath = path.Base(parsed.Path)
	}
	entry := infoXMLEntry{Kind: info.Kind.String(), Path: entryPath, Revision: info.Revision, URL: info.URL,
		Relative:   "^/" + strings.TrimPrefix(info.URL, strings.TrimSuffix(info.RepositoryRoot, "/")+"/"),
		Repository: repositoryXML{Root: info.RepositoryRoot, UUID: info.RepositoryUUID},
		Commit:     commitXML{Revision: info.ChangedRevision, Author: info.ChangedAuthor, Date: formatXMLTime(info.ChangedDate)}}
	if info.Kind == svn.NodeFile && info.Size >= 0 {
		entry.Size = &info.Size
	}
	return entry
}

func printInfo(output io.Writer, info *client.Info) {
	fmt.Fprintf(output, "Path: %s\n", info.Path)
	fmt.Fprintf(output, "Name: %s\n", filepath.Base(info.Path))
	fmt.Fprintf(output, "URL: %s\n", info.URL)
	fmt.Fprintf(output, "Relative URL: ^/%s\n", strings.TrimPrefix(info.URL, strings.TrimSuffix(info.RepositoryRoot, "/")+"/"))
	fmt.Fprintf(output, "Repository Root: %s\n", info.RepositoryRoot)
	fmt.Fprintf(output, "Repository UUID: %s\n", info.RepositoryUUID)
	fmt.Fprintf(output, "Revision: %d\n", info.Revision)
	fmt.Fprintf(output, "Node Kind: %s\n", info.Kind)
	fmt.Fprintf(output, "Last Changed Author: %s\n", info.ChangedAuthor)
	fmt.Fprintf(output, "Last Changed Rev: %d\n", info.ChangedRevision)
	if !info.ChangedDate.IsZero() {
		fmt.Fprintf(output, "Last Changed Date: %s\n", info.ChangedDate.Local().Format("2006-01-02 15:04:05 -0700 (Mon, 02 Jan 2006)"))
	}
	if info.Size >= 0 && info.Kind == svn.NodeFile {
		fmt.Fprintf(output, "Size in Repository: %d\n", info.Size)
	}
	fmt.Fprintln(output)
}

type listXML struct {
	XMLName xml.Name      `xml:"lists"`
	Lists   []listXMLList `xml:"list"`
}

type listXMLList struct {
	Path    string         `xml:"path,attr"`
	Entries []listXMLEntry `xml:"entry"`
}

type listXMLEntry struct {
	Kind   string    `xml:"kind,attr"`
	Name   string    `xml:"name"`
	Size   int64     `xml:"size,omitempty"`
	Commit commitXML `xml:"commit"`
}

func makeListXMLEntry(entry client.ListEntry) listXMLEntry {
	return listXMLEntry{Kind: entry.Entry.Kind.String(), Name: entry.Path, Size: entry.Entry.Size,
		Commit: commitXML{Revision: entry.Entry.CreatedRev, Author: entry.Entry.LastAuthor, Date: formatXMLTime(entry.Entry.Time)}}
}

func displayListPath(entry client.ListEntry) string {
	if entry.Entry.Kind == svn.NodeDir {
		return entry.Path + "/"
	}
	return entry.Path
}

type statusXML struct {
	XMLName xml.Name          `xml:"status"`
	Targets []statusXMLTarget `xml:"target"`
}

type statusXMLTarget struct {
	Path    string           `xml:"path,attr"`
	Entries []statusXMLEntry `xml:"entry"`
}

type statusXMLEntry struct {
	Path     string      `xml:"path,attr"`
	WCStatus wcStatusXML `xml:"wc-status"`
}

type wcStatusXML struct {
	Item       string     `xml:"item,attr"`
	Props      string     `xml:"props,attr"`
	Revision   svn.Revnum `xml:"revision,attr,omitempty"`
	Copied     bool       `xml:"copied,attr,omitempty"`
	Switched   bool       `xml:"switched,attr,omitempty"`
	Tree       bool       `xml:"tree-conflicted,attr,omitempty"`
	Commit     *commitXML `xml:"commit,omitempty"`
	Changelist string     `xml:"changelist,omitempty"`
}

func makeStatusXMLEntry(status *wc.Status) statusXMLEntry {
	entry := statusXMLEntry{Path: status.Path, WCStatus: wcStatusXML{Item: string(status.NodeStatus), Props: string(status.PropertyStatus), Revision: status.Revision, Copied: status.Copied, Switched: status.Switched, Tree: status.TreeConflicted, Changelist: status.Changelist}}
	if status.ChangedRevision.IsValid() {
		entry.WCStatus.Commit = &commitXML{Revision: status.ChangedRevision, Author: status.ChangedAuthor, Date: formatXMLTime(status.ChangedDate)}
	}
	return entry
}

func printStatus(output io.Writer, status *wc.Status, verbose bool) {
	line := []byte("       ")
	line[0] = statusCode(status.NodeStatus)
	line[1] = statusCode(status.PropertyStatus)
	if status.WCInfoLocked {
		line[2] = 'L'
	}
	if status.Copied {
		line[3] = '+'
	}
	if status.Switched {
		line[4] = 'S'
	}
	if status.Conflicted || status.TreeConflicted {
		line[6] = 'C'
	}
	if verbose {
		fmt.Fprintf(output, "%s %8d %8d %-12s %s\n", line, status.Revision, status.ChangedRevision, status.ChangedAuthor, status.Path)
	} else {
		fmt.Fprintf(output, "%s %s\n", line, status.Path)
	}
}

func statusCode(status wc.StatusKind) byte {
	switch status {
	case wc.StatusModified:
		return 'M'
	case wc.StatusAdded:
		return 'A'
	case wc.StatusDeleted:
		return 'D'
	case wc.StatusReplaced:
		return 'R'
	case wc.StatusMissing:
		return '!'
	case wc.StatusObstructed:
		return '~'
	case wc.StatusConflicted:
		return 'C'
	case wc.StatusIgnored:
		return 'I'
	case wc.StatusUnversioned:
		return '?'
	case wc.StatusExternal:
		return 'X'
	default:
		return ' '
	}
}

func formatXMLTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format("2006-01-02T15:04:05.000000Z")
}

func formatHumanTime(value time.Time) string {
	if value.IsZero() {
		return "                   "
	}
	return value.Local().Format("Jan 02 15:04")
}
