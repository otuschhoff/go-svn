package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/otuschhoff/go-svn/client"
	"github.com/otuschhoff/go-svn/svn"
)

func (cmd *command) log(ctx context.Context, args []string) error {
	flags := newFlagSet("log", cmd.stderr)
	xmlOutput := flags.Bool("xml", false, "output in XML")
	verbose := flags.Bool("verbose", false, "show changed paths")
	flags.BoolVar(verbose, "v", false, "show changed paths")
	quiet := flags.Bool("quiet", false, "omit log messages")
	flags.BoolVar(quiet, "q", false, "omit log messages")
	stopOnCopy := flags.Bool("stop-on-copy", false, "stop on copy")
	limit := flags.Int("limit", 0, "maximum entries")
	flags.IntVar(limit, "l", 0, "maximum entries")
	revision := flags.String("revision", "", "revision range")
	flags.StringVar(revision, "r", "", "revision range")
	if err := flags.Parse(args); err != nil {
		return err
	}
	targets := flags.Args()
	if len(targets) == 0 {
		targets = []string{"."}
	}
	ranges, err := parseRevisionRanges(*revision)
	if err != nil {
		return err
	}
	result := logXML{}
	for _, target := range targets {
		err := cmd.client.Log(ctx, target, client.LogOptions{Ranges: ranges, Limit: *limit, DiscoverChangedPaths: *verbose, StopOnCopy: *stopOnCopy}, func(entry *svn.LogEntry) error {
			if *xmlOutput {
				result.Entries = append(result.Entries, makeLogXMLEntry(entry, *verbose))
			} else {
				printLog(cmd.stdout, entry, *verbose, *quiet)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if *xmlOutput {
		return writeXML(cmd.stdout, result)
	}
	return nil
}

func (cmd *command) blame(ctx context.Context, args []string) error {
	flags := newFlagSet("blame", cmd.stderr)
	xmlOutput := flags.Bool("xml", false, "output in XML")
	verbose := flags.Bool("verbose", false, "show dates")
	flags.BoolVar(verbose, "v", false, "show dates")
	revision := flags.String("revision", "", "revision range")
	flags.StringVar(revision, "r", "", "revision range")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return fmt.Errorf("blame requires at least one target")
	}
	start, end, err := parseRevisionRange(*revision)
	if err != nil {
		return err
	}
	result := blameXML{}
	for _, target := range flags.Args() {
		targetResult := blameXMLTarget{Path: target}
		err := cmd.client.Blame(ctx, target, client.BlameOptions{Start: start, End: end}, func(line client.BlameLine) error {
			if *xmlOutput {
				targetResult.Entries = append(targetResult.Entries, blameXMLEntry{LineNumber: line.LineNumber, Commit: commitXML{Revision: line.Revision, Author: line.Author, Date: formatXMLTime(line.Date)}})
			} else if *verbose {
				fmt.Fprintf(cmd.stdout, "%6d %-10s %s %s\n", line.Revision, line.Author, formatXMLTime(line.Date), line.Line)
			} else {
				fmt.Fprintf(cmd.stdout, "%6d %-10s %s\n", line.Revision, line.Author, line.Line)
			}
			return nil
		})
		if err != nil {
			return err
		}
		result.Targets = append(result.Targets, targetResult)
	}
	if *xmlOutput {
		return writeXML(cmd.stdout, result)
	}
	return nil
}

func (cmd *command) diff(ctx context.Context, args []string) error {
	flags := newFlagSet("diff", cmd.stderr)
	xmlOutput := flags.Bool("xml", false, "output summary in XML")
	summarize := flags.Bool("summarize", false, "show only changed paths")
	flags.BoolVar(summarize, "summarise", false, "show only changed paths")
	revision := flags.String("revision", "", "revision range")
	flags.StringVar(revision, "r", "", "revision range")
	git := flags.Bool("git", false, "use git diff format")
	if err := flags.Parse(args); err != nil {
		return err
	}
	targets := flags.Args()
	if len(targets) == 0 {
		targets = []string{"."}
	}
	leftRevision, rightRevision, err := parseDiffRevisions(*revision)
	if err != nil {
		return err
	}
	result := diffSummaryXML{}
	for _, target := range targets {
		left := client.DiffTarget{Target: target, Revision: leftRevision}
		right := client.DiffTarget{Target: target, Revision: rightRevision}
		options := client.DiffOptions{Depth: svn.DepthInfinity, Git: *git}
		if *summarize || *xmlOutput {
			err := cmd.client.DiffSummarize(ctx, left, right, options, func(summary client.DiffSummary) error {
				if *xmlOutput {
					result.Paths = append(result.Paths, diffSummaryXMLPath{Kind: summary.Kind.String(), Item: diffItem(summary.NodeStatus, summary.TextModified), Props: propsItem(summary.PropertiesChanged), Path: summaryPath(target, summary.Path)})
				} else {
					prop := ' '
					if summary.PropertiesChanged {
						prop = 'M'
					}
					fmt.Fprintf(cmd.stdout, "%c%c      %s\n", summary.NodeStatus, prop, summary.Path)
				}
				return nil
			})
			if err != nil {
				return err
			}
		} else if err := cmd.client.Diff(ctx, left, right, cmd.stdout, options); err != nil {
			return err
		}
	}
	if *xmlOutput {
		return writeXML(cmd.stdout, result)
	}
	return nil
}

func (cmd *command) proplist(ctx context.Context, args []string) error {
	flags := newFlagSet("proplist", cmd.stderr)
	xmlOutput := flags.Bool("xml", false, "output in XML")
	verbose := flags.Bool("verbose", false, "show property values")
	flags.BoolVar(verbose, "v", false, "show property values")
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
	result := propertiesXML{}
	for _, target := range targets {
		properties, err := cmd.client.PropList(ctx, target, client.PropertyOptions{InfoOptions: client.InfoOptions{Revision: parsedRevision}})
		if err != nil {
			return err
		}
		names := sortedPropertyNames(properties)
		if *xmlOutput {
			xmlTarget := propertiesXMLTarget{Path: target}
			for _, name := range names {
				property := propertyXML{Name: name}
				if *verbose {
					value := string(properties[name])
					property.Value = &value
				}
				xmlTarget.Properties = append(xmlTarget.Properties, property)
			}
			result.Targets = append(result.Targets, xmlTarget)
		} else {
			fmt.Fprintf(cmd.stdout, "Properties on '%s':\n", target)
			for _, name := range names {
				fmt.Fprintf(cmd.stdout, "  %s\n", name)
				if *verbose {
					fmt.Fprintf(cmd.stdout, "    %s\n", properties[name])
				}
			}
		}
	}
	if *xmlOutput {
		return writeXML(cmd.stdout, result)
	}
	return nil
}

func (cmd *command) propget(ctx context.Context, args []string) error {
	flags := newFlagSet("propget", cmd.stderr)
	xmlOutput := flags.Bool("xml", false, "output in XML")
	revision := flags.String("revision", "", "operative revision")
	flags.StringVar(revision, "r", "", "operative revision")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() < 2 {
		return fmt.Errorf("propget requires a property name and target")
	}
	parsedRevision, err := parseRevision(*revision)
	if err != nil {
		return err
	}
	name := flags.Arg(0)
	result := propertiesXML{}
	for _, target := range flags.Args()[1:] {
		value, found, err := cmd.client.PropGet(ctx, target, name, client.PropertyOptions{InfoOptions: client.InfoOptions{Revision: parsedRevision}})
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("property %q not found on %s", name, target)
		}
		if *xmlOutput {
			text := string(value)
			result.Targets = append(result.Targets, propertiesXMLTarget{Path: target, Properties: []propertyXML{{Name: name, Value: &text}}})
		} else {
			if _, err := cmd.stdout.Write(value); err != nil {
				return err
			}
			if len(value) == 0 || value[len(value)-1] != '\n' {
				fmt.Fprintln(cmd.stdout)
			}
		}
	}
	if *xmlOutput {
		return writeXML(cmd.stdout, result)
	}
	return nil
}

func parseRevisionRanges(value string) ([]svn.RevisionRange, error) {
	if value == "" {
		return nil, nil
	}
	var ranges []svn.RevisionRange
	for _, item := range strings.Split(value, ",") {
		start, end, err := parseRevisionRange(item)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(item, ":") {
			end = start
		}
		ranges = append(ranges, svn.RevisionRange{Start: start, End: end})
	}
	return ranges, nil
}

func parseRevisionRange(value string) (svn.Revision, svn.Revision, error) {
	if value == "" {
		return svn.Revision{}, svn.Revision{}, nil
	}
	parts := strings.SplitN(value, ":", 2)
	start, err := svn.ParseRevision(parts[0])
	if err != nil {
		return svn.Revision{}, svn.Revision{}, err
	}
	if len(parts) == 1 {
		return start, svn.Revision{}, nil
	}
	end, err := svn.ParseRevision(parts[1])
	return start, end, err
}

func parseDiffRevisions(value string) (svn.Revision, svn.Revision, error) {
	if value == "" {
		return svn.Revision{Kind: svn.RevisionBase}, svn.Revision{Kind: svn.RevisionWorking}, nil
	}
	start, end, err := parseRevisionRange(value)
	if err != nil {
		return svn.Revision{}, svn.Revision{}, err
	}
	if !strings.Contains(value, ":") {
		if start.Kind != svn.RevisionNumber || start.Number == 0 {
			return svn.Revision{}, svn.Revision{}, fmt.Errorf("single diff revision must be a positive number")
		}
		end = start
		start.Number--
	}
	return start, end, nil
}

type logXML struct {
	XMLName xml.Name      `xml:"log"`
	Entries []logXMLEntry `xml:"logentry"`
}

type logXMLEntry struct {
	Revision svn.Revnum   `xml:"revision,attr"`
	Author   string       `xml:"author,omitempty"`
	Date     string       `xml:"date,omitempty"`
	Paths    []logXMLPath `xml:"paths>path,omitempty"`
	Message  string       `xml:"msg"`
}

type logXMLPath struct {
	Action       string      `xml:"action,attr"`
	Kind         string      `xml:"kind,attr,omitempty"`
	TextMods     *string     `xml:"text-mods,attr,omitempty"`
	PropMods     *string     `xml:"prop-mods,attr,omitempty"`
	CopyfromPath string      `xml:"copyfrom-path,attr,omitempty"`
	CopyfromRev  *svn.Revnum `xml:"copyfrom-rev,attr,omitempty"`
	Path         string      `xml:",chardata"`
}

func makeLogXMLEntry(entry *svn.LogEntry, changedPaths bool) logXMLEntry {
	result := logXMLEntry{Revision: entry.Revision, Author: entry.Author, Date: formatXMLTime(entry.Date), Message: entry.Message}
	if changedPaths {
		for _, change := range entry.ChangedPaths {
			path := logXMLPath{Action: string(change.Action), Kind: change.NodeKind.String(), TextMods: tristateXML(change.TextModified), PropMods: tristateXML(change.PropsModified), CopyfromPath: change.CopyfromPath, Path: change.Path}
			if change.CopyfromPath != "" && change.CopyfromRev.IsValid() {
				revision := change.CopyfromRev
				path.CopyfromRev = &revision
			}
			result.Paths = append(result.Paths, path)
		}
	}
	return result
}

func printLog(output interface{ Write([]byte) (int, error) }, entry *svn.LogEntry, verbose, quiet bool) {
	fmt.Fprintln(output, strings.Repeat("-", 72))
	lines := 0
	if entry.Message != "" {
		lines = strings.Count(entry.Message, "\n") + 1
	}
	fmt.Fprintf(output, "r%d | %s | %s | %d line", entry.Revision, entry.Author, formatXMLTime(entry.Date), lines)
	if lines != 1 {
		fmt.Fprint(output, "s")
	}
	fmt.Fprintln(output)
	if verbose && len(entry.ChangedPaths) != 0 {
		fmt.Fprintln(output, "Changed paths:")
		for _, change := range entry.ChangedPaths {
			fmt.Fprintf(output, "   %c %s\n", change.Action, change.Path)
		}
	}
	if !quiet {
		fmt.Fprintf(output, "\n%s\n", entry.Message)
	}
}

type blameXML struct {
	XMLName xml.Name         `xml:"blame"`
	Targets []blameXMLTarget `xml:"target"`
}

type blameXMLTarget struct {
	Path    string          `xml:"path,attr"`
	Entries []blameXMLEntry `xml:"entry"`
}

type blameXMLEntry struct {
	LineNumber int       `xml:"line-number,attr"`
	Commit     commitXML `xml:"commit"`
}

type diffSummaryXML struct {
	XMLName xml.Name             `xml:"diff"`
	Paths   []diffSummaryXMLPath `xml:"paths>path"`
}

type diffSummaryXMLPath struct {
	Kind  string `xml:"kind,attr"`
	Item  string `xml:"item,attr"`
	Props string `xml:"props,attr"`
	Path  string `xml:",chardata"`
}

func diffItem(status byte, textModified bool) string {
	if !textModified {
		return "none"
	}
	switch status {
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'M':
		return "modified"
	case 'R':
		return "replaced"
	default:
		return "none"
	}
}

func propsItem(changed bool) string {
	if changed {
		return "modified"
	}
	return "none"
}

type propertiesXML struct {
	XMLName xml.Name              `xml:"properties"`
	Targets []propertiesXMLTarget `xml:"target"`
}

type propertiesXMLTarget struct {
	Path       string        `xml:"path,attr"`
	Properties []propertyXML `xml:"property"`
}

type propertyXML struct {
	Name  string  `xml:"name,attr"`
	Value *string `xml:",chardata"`
}

func tristateXML(value svn.Tristate) *string {
	if value == svn.TristateUnknown {
		return nil
	}
	text := strconv.FormatBool(value == svn.TristateTrue)
	return &text
}

func summaryPath(target, relative string) string {
	if strings.Contains(target, "://") {
		return strings.TrimRight(target, "/") + "/" + strings.TrimLeft(relative, "/")
	}
	return filepath.Join(target, relative)
}

func sortedPropertyNames(properties svn.Props) []string {
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func parsePositiveInt(value string) (int, error) {
	number, err := strconv.Atoi(value)
	if err != nil || number < 0 {
		return 0, fmt.Errorf("invalid non-negative integer %q", value)
	}
	return number, nil
}
