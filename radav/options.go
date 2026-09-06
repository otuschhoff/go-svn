package radav

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/oliver-tuschhoff/go-svn/ra"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

type ServerInfo struct {
	RepositoryRoot string
	UUID           string
	Youngest       svn.Revnum
	MeResource     string
	RevRootStub    string
	RevStub        string
	TxnRootStub    string
	TxnStub        string
	VTxnRootStub   string
	VTxnStub       string
	BulkUpdates    string
	SupportedPosts map[string]bool
	Capabilities   map[ra.Capability]bool
}

var capabilityURIs = map[string]ra.Capability{
	"depth": ra.CapabilityDepth, "mergeinfo": ra.CapabilityMergeinfo,
	"log-revprops": ra.CapabilityLogRevprops, "partial-replay": ra.CapabilityPartialReplay,
	"atomic-revprops": ra.CapabilityAtomicRevprops, "inherited-props": ra.CapabilityInheritedProps,
	"ephemeral-txnprops": ra.CapabilityEphemeralTxnprops, "reverse-file-revs": ra.CapabilityFileRevsReverse,
	"list": ra.CapabilityList, "svndiff1": ra.CapabilitySvndiff1, "svndiff2": ra.CapabilitySvndiff2,
}

func discover(ctx context.Context, transport *transport, rawURL string) (ServerInfo, string, error) {
	body := []byte(`<D:options xmlns:D="DAV:"><D:activity-collection-set/></D:options>`)
	response, err := transport.request(ctx, "OPTIONS", rawURL, body, http.Header{"Content-Type": []string{contentXML}})
	if err != nil {
		return ServerInfo{}, rawURL, err
	}
	defer drainAndClose(response.Body)
	corrected := response.Request.URL.String()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ServerInfo{}, corrected, responseError(response)
	}
	info := ServerInfo{
		UUID: response.Header.Get("SVN-Repository-UUID"), MeResource: response.Header.Get("SVN-Me-Resource"),
		RevRootStub: response.Header.Get("SVN-Rev-Root-Stub"), RevStub: response.Header.Get("SVN-Rev-Stub"),
		TxnRootStub: response.Header.Get("SVN-Txn-Root-Stub"), TxnStub: response.Header.Get("SVN-Txn-Stub"),
		VTxnRootStub: response.Header.Get("SVN-VTxn-Root-Stub"), VTxnStub: response.Header.Get("SVN-VTxn-Stub"),
		BulkUpdates: response.Header.Get("SVN-Allow-Bulk-Updates"), SupportedPosts: make(map[string]bool), Capabilities: make(map[ra.Capability]bool),
	}
	root := response.Header.Get("SVN-Repository-Root")
	if root == "" {
		root = corrected
	}
	info.RepositoryRoot, err = resolveURL(corrected, root)
	if err != nil {
		return ServerInfo{}, corrected, err
	}
	if raw := response.Header.Get("SVN-Youngest-Rev"); raw != "" {
		number, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || number < 0 {
			return ServerInfo{}, corrected, fmt.Errorf("%w: invalid youngest revision %q", svn.ErrRADAVResponseHeaderBadness, raw)
		}
		info.Youngest = svn.Revnum(number)
	} else {
		info.Youngest = svn.InvalidRevnum
	}
	for _, token := range strings.FieldsFunc(response.Header.Get("SVN-Supported-Posts"), func(r rune) bool { return r == ',' || r == ' ' }) {
		info.SupportedPosts[token] = true
	}
	for _, value := range response.Header.Values("DAV") {
		for _, token := range strings.Split(value, ",") {
			token = strings.TrimSpace(token)
			for suffix, capability := range capabilityURIs {
				if strings.HasSuffix(token, "/"+suffix) {
					info.Capabilities[capability] = true
				}
			}
		}
	}
	return info, corrected, nil
}

func resolveURL(base, reference string) (string, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	referenceURL, err := url.Parse(reference)
	if err != nil {
		return "", err
	}
	referenceURL.User = nil
	return baseURL.ResolveReference(referenceURL).String(), nil
}
