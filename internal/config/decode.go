package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// idPattern is the form of tenant, identity and server ids (spec 5.1). Tags
// use it too.
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// plainKey is a mapping key that can be written in a path without quotes.
var plainKey = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// maxEcho bounds how much of a hostile key is echoed in a path.
const maxEcho = 64

// decoder walks a yaml.Node tree and collects problems. It never stops at the
// first problem.
type decoder struct {
	probs []Problem
}

func (d *decoder) add(path string, n *yaml.Node, format string, args ...any) {
	line := 0
	if n != nil {
		line = n.Line
	}
	d.probs = append(d.probs, Problem{Path: path, Line: line, Message: fmt.Sprintf(format, args...)})
}

func join(path, key string) string {
	if !plainKey.MatchString(key) {
		if len(key) > maxEcho {
			key = key[:maxEcho]
		}
		key = strconv.Quote(key)
	}
	if path == "" {
		return key
	}
	return path + "." + key
}

// itemPath is the path of item i of a list: base[i], plus (id) when the item
// is a mapping with a valid id.
func itemPath(base string, i int, n *yaml.Node) string {
	p := fmt.Sprintf("%s[%d]", base, i)
	if n == nil || n.Kind != yaml.MappingNode {
		return p
	}
	for j := 0; j+1 < len(n.Content); j += 2 {
		k, v := n.Content[j], n.Content[j+1]
		if k.Kind == yaml.ScalarNode && k.Value == "id" && v.Kind == yaml.ScalarNode && idPattern.MatchString(v.Value) {
			return p + "(" + v.Value + ")"
		}
	}
	return p
}

func kindName(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "a mapping"
	case yaml.SequenceNode:
		return "a sequence"
	case yaml.AliasNode:
		return "an alias"
	case yaml.ScalarNode:
		switch n.ShortTag() {
		case "!!str":
			return "a string"
		case "!!int":
			return "an integer"
		case "!!bool":
			return "a boolean"
		case "!!float":
			return "a number"
		case "!!null":
			return "null"
		}
		return "a value tagged " + n.ShortTag()
	default:
		return "something else"
	}
}

// usable reports anchors and aliases, which this format does not support,
// and returns false for an alias.
func (d *decoder) usable(path string, n *yaml.Node) bool {
	if n.Anchor != "" {
		d.add(path, n, "anchors are not supported")
	}
	if n.Kind == yaml.AliasNode {
		d.add(path, n, "aliases are not supported")
		return false
	}
	return true
}

func isNull(n *yaml.Node) bool {
	return n == nil || (n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null" && n.Anchor == "")
}

// entry is one key and value of a mapping.
type entry struct {
	key   string
	knode *yaml.Node
	val   *yaml.Node
}

// entries returns the key/value pairs of a mapping in file order, reporting
// keys that are not plain strings and keys that appear twice. A null node is
// an empty mapping. ok is false when n is not a mapping.
func (d *decoder) entries(path string, n *yaml.Node) (es []entry, ok bool) {
	if isNull(n) {
		return nil, true
	}
	if !d.usable(path, n) {
		return nil, false
	}
	if n.Kind != yaml.MappingNode {
		d.add(path, n, "expected a mapping, got %s", kindName(n))
		return nil, false
	}
	first := map[string]int{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind != yaml.ScalarNode || k.ShortTag() != "!!str" {
			d.add(path, k, "keys must be plain strings, got %s", kindName(k))
			continue
		}
		kp := join(path, k.Value)
		if k.Anchor != "" {
			d.add(kp, k, "anchors are not supported")
		}
		if line, dup := first[k.Value]; dup {
			d.add(kp, k, "duplicate key (first at line %d)", line)
			continue
		}
		first[k.Value] = k.Line
		es = append(es, entry{key: k.Value, knode: k, val: v})
	}
	return es, true
}

// fields is a mapping with a fixed key set.
type fields struct {
	d    *decoder
	path string
	node *yaml.Node
	vals map[string]*yaml.Node
}

// mapping decodes n as a mapping whose keys must be among keys. A null node
// is an empty mapping. It returns nil when n is not a mapping.
func (d *decoder) mapping(path string, n *yaml.Node, keys ...string) *fields {
	es, ok := d.entries(path, n)
	if !ok {
		return nil
	}
	f := &fields{d: d, path: path, node: n, vals: map[string]*yaml.Node{}}
	for _, e := range es {
		known := false
		for _, k := range keys {
			if e.key == k {
				known = true
				break
			}
		}
		if !known {
			d.add(join(path, e.key), e.knode, "unknown key")
			continue
		}
		f.vals[e.key] = e.val
	}
	return f
}

// get returns the value of key, or nil when it is absent or null; a missing
// required key is reported.
func (f *fields) get(key string, required bool) *yaml.Node {
	v := f.vals[key]
	if isNull(v) {
		if required {
			f.d.add(join(f.path, key), f.lineNode(key), "is required")
		}
		return nil
	}
	if !f.d.usable(join(f.path, key), v) {
		return nil
	}
	return v
}

// lineNode is the node to take a line from for key: its value if present,
// else the mapping itself.
func (f *fields) lineNode(key string) *yaml.Node {
	if v, ok := f.vals[key]; ok && v != nil {
		return v
	}
	return f.node
}

func (f *fields) str(key string, required bool) (string, bool) {
	v := f.get(key, required)
	if v == nil {
		return "", false
	}
	return f.d.scalarString(join(f.path, key), v)
}

func (d *decoder) scalarString(path string, v *yaml.Node) (string, bool) {
	if v.Kind != yaml.ScalarNode || v.ShortTag() != "!!str" {
		d.add(path, v, "expected a string, got %s", kindName(v))
		return "", false
	}
	if !utf8.ValidString(v.Value) || strings.ContainsRune(v.Value, 0) {
		d.add(path, v, "must be valid UTF-8 without NUL")
		return "", false
	}
	return v.Value, true
}

func (f *fields) integer(key string, required bool) (int64, bool) {
	v := f.get(key, required)
	if v == nil {
		return 0, false
	}
	p := join(f.path, key)
	if v.Kind != yaml.ScalarNode || v.ShortTag() != "!!int" {
		f.d.add(p, v, "expected an integer, got %s", kindName(v))
		return 0, false
	}
	var n int64
	if err := v.Decode(&n); err != nil {
		f.d.add(p, v, "is not a 64-bit integer")
		return 0, false
	}
	return n, true
}

func (f *fields) boolean(key string) (val, ok bool) {
	v := f.get(key, false)
	if v == nil {
		return false, false
	}
	if v.Kind != yaml.ScalarNode || v.ShortTag() != "!!bool" {
		f.d.add(join(f.path, key), v, "expected a boolean, got %s", kindName(v))
		return false, false
	}
	var b bool
	if err := v.Decode(&b); err != nil {
		f.d.add(join(f.path, key), v, "is not a boolean")
		return false, false
	}
	return b, true
}

func (f *fields) seq(key string, required bool) []*yaml.Node {
	v := f.get(key, required)
	if v == nil {
		return nil
	}
	if v.Kind != yaml.SequenceNode {
		f.d.add(join(f.path, key), v, "expected a sequence, got %s", kindName(v))
		return nil
	}
	if required && len(v.Content) == 0 {
		f.d.add(join(f.path, key), v, "must not be empty")
	}
	return v.Content
}

// strings decodes a sequence of strings under key; each item is usable and
// a string, or reported.
func (f *fields) strings(key string) (out, paths []string, nodes []*yaml.Node) {
	base := join(f.path, key)
	for i, n := range f.seq(key, false) {
		p := fmt.Sprintf("%s[%d]", base, i)
		if !f.d.usable(p, n) {
			continue
		}
		s, ok := f.d.scalarString(p, n)
		if !ok {
			continue
		}
		out, paths, nodes = append(out, s), append(paths, p), append(nodes, n)
	}
	return out, paths, nodes
}

// fileRef is a file the loader must check on disk after decoding.
type fileRef struct {
	path string // the problem path
	line int
	file string
	// For a known_hosts file: the host and port it must list.
	host string
	port int
}

// fileRefs are the file checks Load runs after decoding.
type fileRefs struct {
	privateKeys []fileRef
	knownHosts  []fileRef
}

// decode parses data and validates everything that does not need the file
// system. cfg is meaningful only when probs is empty.
func decode(data []byte) (cfg *Config, refs fileRefs, probs []Problem) {
	d := &decoder{}
	cfg = &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			d.add("config", nil, "the file is empty")
		} else {
			d.add("config", nil, "%s", yamlMessage(err))
		}
		return cfg, refs, d.probs
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		d.add("config", &extra, "the file must hold exactly one YAML document")
	}
	if len(doc.Content) != 1 {
		d.add("config", &doc, "the file is empty")
		return cfg, refs, d.probs
	}
	root := doc.Content[0]
	if isNull(root) {
		d.add("config", root, "the file is empty")
		return cfg, refs, d.probs
	}
	top := d.mapping("", root, "version", "server", "auth", "tenants")
	if top == nil {
		return cfg, refs, d.probs
	}
	if v, ok := top.integer("version", true); ok {
		if v != 1 {
			d.add("version", top.vals["version"], "must be 1")
		} else {
			cfg.Version = 1
		}
	}
	cfg.Gateway = d.serverSettings(top)
	cfg.Auth = d.auth(top)
	cfg.Tenants = d.tenants(top, &refs)
	return cfg, refs, d.probs
}

// yamlMessage keeps the parser's own message (it carries the line) without
// the "yaml: " prefix.
func yamlMessage(err error) string {
	return strings.TrimPrefix(err.Error(), "yaml: ")
}

func (d *decoder) serverSettings(top *fields) ServerSettings {
	var s ServerSettings
	n := top.get("server", true)
	if n == nil {
		return s
	}
	f := d.mapping("server", n, "listen", "allow_non_loopback", "data_dir", "audit_log")
	if f == nil {
		return s
	}
	s.AllowNonLoopback, _ = f.boolean("allow_non_loopback")
	if listen, ok := f.str("listen", true); ok {
		if msg := checkListen(listen, s.AllowNonLoopback); msg != "" {
			d.add("server.listen", f.vals["listen"], "%s", msg)
		} else {
			s.Listen = listen
		}
	}
	s.DataDir = d.absPath(f, "data_dir", true)
	s.AuditLog = d.absPath(f, "audit_log", true)
	return s
}

func (d *decoder) absPath(f *fields, key string, required bool) string {
	p, ok := f.str(key, required)
	if !ok {
		return ""
	}
	if msg := checkAbsPath(p); msg != "" {
		d.add(join(f.path, key), f.vals[key], "%s", msg)
		return ""
	}
	return p
}

func (d *decoder) auth(top *fields) Auth {
	var a Auth
	n := top.get("auth", true)
	if n == nil {
		return a
	}
	f := d.mapping("auth", n, "provider", "cloudflare_access")
	if f == nil {
		return a
	}
	if p, ok := f.str("provider", true); ok {
		if p != ProviderCloudflareAccess {
			d.add("auth.provider", f.vals["provider"], "must be %q", ProviderCloudflareAccess)
		} else {
			a.Provider = p
		}
	}
	cn := f.get("cloudflare_access", true)
	if cn == nil {
		return a
	}
	c := d.mapping("auth.cloudflare_access", cn, "team_domain", "audience", "certs_url")
	if c == nil {
		return a
	}
	if td, ok := c.str("team_domain", true); ok {
		if !isHostname(td) {
			d.add("auth.cloudflare_access.team_domain", c.vals["team_domain"], "must be a DNS host name")
		} else {
			a.CloudflareAccess.TeamDomain = td
		}
	}
	if aud, ok := c.str("audience", true); ok {
		if msg := checkToken(aud, 512); msg != "" {
			d.add("auth.cloudflare_access.audience", c.vals["audience"], "%s", msg)
		} else {
			a.CloudflareAccess.Audience = aud
		}
	}
	if u, ok := c.str("certs_url", false); ok {
		if msg := checkCertsURL(u); msg != "" {
			d.add("auth.cloudflare_access.certs_url", c.vals["certs_url"], "%s", msg)
		} else {
			a.CloudflareAccess.CertsURL = u
		}
	}
	return a
}

func (d *decoder) tenants(top *fields, refs *fileRefs) []tenant.Tenant {
	items := top.seq("tenants", true)
	if len(items) > MaxTenants {
		d.add("tenants", top.vals["tenants"], "at most %d tenant is supported in this version (MaxTenants), got %d", MaxTenants, len(items))
	}
	var out []tenant.Tenant
	seen := map[string]int{}
	for i, n := range items {
		p := itemPath("tenants", i, n)
		t, ok := d.tenant(p, n, refs)
		if !ok {
			continue
		}
		if prev, dup := seen[string(t.ID)]; dup {
			d.add(p+".id", n, "duplicate tenant id (also tenants[%d])", prev)
		}
		seen[string(t.ID)] = i
		out = append(out, t)
	}
	return out
}

func (d *decoder) id(f *fields, what string) (string, bool) {
	s, ok := f.str("id", true)
	if !ok {
		return "", false
	}
	if !idPattern.MatchString(s) {
		d.add(join(f.path, "id"), f.vals["id"], "%s id must match %s", what, idPattern)
		return "", false
	}
	return s, true
}

func (d *decoder) tenant(p string, n *yaml.Node, refs *fileRefs) (tenant.Tenant, bool) {
	var t tenant.Tenant
	if !d.usable(p, n) {
		return t, false
	}
	if isNull(n) {
		d.add(p, n, "expected a mapping, got null")
		return t, false
	}
	f := d.mapping(p, n, "id", "principals", "identities", "servers", "policy", "limits")
	if f == nil {
		return t, false
	}
	id, idOK := d.id(f, "tenant")
	t.ID = tenant.TenantID(id)
	t.Principals = d.principals(f)
	t.Identities = d.identities(f, refs)
	t.Servers = d.servers(f, t.ID, t.Identities, refs)
	t.Policy = d.policy(f)
	t.Limits = d.limits(f)
	return t, idOK
}

func (d *decoder) principals(tf *fields) []tenant.PrincipalMatch {
	var out []tenant.PrincipalMatch
	base := join(tf.path, "principals")
	seen := map[string]string{}
	for i, n := range tf.seq("principals", true) {
		p := fmt.Sprintf("%s[%d]", base, i)
		if !d.usable(p, n) {
			continue
		}
		if isNull(n) {
			d.add(p, n, "expected a mapping, got null")
			continue
		}
		f := d.mapping(p, n, "email", "common_name")
		if f == nil {
			continue
		}
		email, hasEmail := f.str("email", false)
		cn, hasCN := f.str("common_name", false)
		_, emailPresent := f.vals["email"]
		_, cnPresent := f.vals["common_name"]
		if emailPresent == cnPresent {
			d.add(p, n, "needs exactly one of email or common_name")
			continue
		}
		var m tenant.PrincipalMatch
		var key string
		switch {
		case hasEmail:
			if !isEmail(email) {
				d.add(p+".email", f.vals["email"], "must be an email address")
				continue
			}
			m.Email, key = email, "email:"+strings.ToLower(email)
		case hasCN:
			if msg := checkToken(cn, 256); msg != "" {
				d.add(p+".common_name", f.vals["common_name"], "%s", msg)
				continue
			}
			m.CommonName, key = cn, "cn:"+cn
		default:
			continue
		}
		if prev, dup := seen[key]; dup {
			d.add(p, n, "duplicate principal (also %s)", prev)
			continue
		}
		seen[key] = p
		out = append(out, m)
	}
	return out
}

var userPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,31}$`)

func (d *decoder) identities(tf *fields, refs *fileRefs) map[tenant.IdentityID]tenant.Identity {
	out := map[tenant.IdentityID]tenant.Identity{}
	n := tf.get("identities", true)
	if n == nil {
		return out
	}
	base := join(tf.path, "identities")
	es, ok := d.entries(base, n)
	if !ok {
		return out
	}
	if len(es) == 0 {
		d.add(base, n, "must not be empty")
	}
	for _, e := range es {
		p := join(base, e.key)
		if !idPattern.MatchString(e.key) {
			d.add(p, e.knode, "identity id must match %s", idPattern)
			continue
		}
		if isNull(e.val) {
			d.add(p, e.val, "expected a mapping, got null")
			continue
		}
		f := d.mapping(p, e.val, "user", "private_key_file")
		if f == nil {
			continue
		}
		id := tenant.Identity{ID: tenant.IdentityID(e.key)}
		if u, ok := f.str("user", true); ok {
			if !userPattern.MatchString(u) {
				d.add(p+".user", f.vals["user"], "must be a login name matching %s", userPattern)
			} else {
				id.User = u
			}
		}
		id.PrivateKeyFile = d.absPath(f, "private_key_file", true)
		if id.PrivateKeyFile != "" {
			refs.privateKeys = append(refs.privateKeys, fileRef{path: p + ".private_key_file", line: f.vals["private_key_file"].Line, file: id.PrivateKeyFile})
		}
		out[id.ID] = id
	}
	return out
}

const (
	maxDescription = 256
	maxTags        = 16
)

func (d *decoder) servers(tf *fields, tid tenant.TenantID, ids map[tenant.IdentityID]tenant.Identity, refs *fileRefs) []tenant.Server {
	var out []tenant.Server
	base := join(tf.path, "servers")
	seen := map[string]int{}
	for i, n := range tf.seq("servers", true) {
		p := itemPath(base, i, n)
		if !d.usable(p, n) {
			continue
		}
		if isNull(n) {
			d.add(p, n, "expected a mapping, got null")
			continue
		}
		f := d.mapping(p, n, "id", "description", "host", "port", "identity", "host_key", "known_hosts_file", "access", "tags")
		if f == nil {
			continue
		}
		s := tenant.Server{Tenant: tid, Port: DefaultSSHPort}
		if id, ok := d.id(f, "server"); ok {
			if prev, dup := seen[id]; dup {
				d.add(p+".id", f.vals["id"], "duplicate server id (also %s[%d])", base, prev)
			}
			seen[id] = i
			s.ID = tenant.ServerID(id)
		}
		if desc, ok := f.str("description", false); ok {
			if len(desc) > maxDescription || hasControl(desc) {
				d.add(p+".description", f.vals["description"], "must be at most %d bytes without control characters", maxDescription)
			} else {
				s.Description = desc
			}
		}
		if h, ok := f.str("host", true); ok {
			if !isHost(h) {
				d.add(p+".host", f.vals["host"], "must be a DNS host name or an IP address")
			} else {
				s.Host = h
			}
		}
		if port, ok := f.integer("port", false); ok {
			if port < 1 || port > 65535 {
				d.add(p+".port", f.vals["port"], "must be between 1 and 65535")
			} else {
				s.Port = int(port)
			}
		}
		if ref, ok := f.str("identity", true); ok {
			if id, found := ids[tenant.IdentityID(ref)]; found {
				s.Identity = id
			} else {
				d.add(p+".identity", f.vals["identity"], "names an identity that does not exist in this tenant")
			}
		}
		d.hostKey(f, p, &s, refs)
		if a, ok := f.str("access", true); ok {
			switch a {
			case "look":
				s.Access = tenant.AccessLook
			case "full":
				d.add(p+".access", f.vals["access"], "access level full is not supported in this version")
			default:
				d.add(p+".access", f.vals["access"], "must be \"look\"")
			}
		}
		tags, tagPaths, tagNodes := f.strings("tags")
		if len(tags) > maxTags {
			d.add(p+".tags", f.vals["tags"], "at most %d tags", maxTags)
		}
		seenTag := map[string]bool{}
		for j, tag := range tags {
			switch {
			case !idPattern.MatchString(tag):
				d.add(tagPaths[j], tagNodes[j], "tag must match %s", idPattern)
			case seenTag[tag]:
				d.add(tagPaths[j], tagNodes[j], "duplicate tag")
			default:
				seenTag[tag] = true
				s.Tags = append(s.Tags, tag)
			}
		}
		out = append(out, s)
	}
	return out
}

func (d *decoder) hostKey(f *fields, p string, s *tenant.Server, refs *fileRefs) {
	_, hasKey := f.vals["host_key"]
	_, hasFile := f.vals["known_hosts_file"]
	if hasKey == hasFile {
		d.add(p, f.node, "needs exactly one of host_key or known_hosts_file")
		return
	}
	if hasKey {
		if k, ok := f.str("host_key", true); ok {
			norm, msg := parseHostKey(k)
			if msg != "" {
				d.add(p+".host_key", f.vals["host_key"], "%s", msg)
			} else {
				s.HostKey = norm
			}
		}
		return
	}
	s.KnownHostsFile = d.absPath(f, "known_hosts_file", true)
	if s.KnownHostsFile != "" && s.Host != "" {
		refs.knownHosts = append(refs.knownHosts, fileRef{
			path: p + ".known_hosts_file", line: f.vals["known_hosts_file"].Line,
			file: s.KnownHostsFile, host: s.Host, port: s.Port,
		})
	}
}

// maxPattern bounds an operator pattern's length.
const maxPattern = 1024

func (d *decoder) policy(tf *fields) tenant.Policy {
	var pol tenant.Policy
	n := tf.get("policy", false)
	if n == nil {
		return pol
	}
	f := d.mapping(join(tf.path, "policy"), n, "deny_commands", "deny_paths")
	if f == nil {
		return pol
	}
	cmds, paths, nodes := f.strings("deny_commands")
	for i, c := range cmds {
		if msg := checkCommandPattern(c); msg != "" {
			d.add(paths[i], nodes[i], "%s", msg)
			continue
		}
		pol.DenyCommands = append(pol.DenyCommands, c)
	}
	globs, paths, nodes := f.strings("deny_paths")
	for i, g := range globs {
		if msg := checkPathGlob(g); msg != "" {
			d.add(paths[i], nodes[i], "%s", msg)
			continue
		}
		pol.DenyPaths = append(pol.DenyPaths, g)
	}
	return pol
}

func (d *decoder) limits(tf *fields) tenant.Limits {
	l := DefaultLimits()
	n := tf.get("limits", false)
	keys := make([]string, len(limitTable))
	for i, s := range limitTable {
		keys[i] = s.key
	}
	base := join(tf.path, "limits")
	f := d.mapping(base, n, keys...)
	if f == nil {
		return l
	}
	valid := map[string]bool{}
	for _, s := range limitTable {
		v, ok := f.integer(s.key, false)
		if !ok {
			// Absent keeps the default; a bad value was reported.
			_, present := f.vals[s.key]
			valid[s.key] = !present
			continue
		}
		if v < s.min || v > s.max {
			d.add(join(base, s.key), f.vals[s.key], "must be between %d and %d", s.min, s.max)
			continue
		}
		*s.field(&l) = v
		valid[s.key] = true
	}
	for _, pr := range limitPairs {
		if !valid[pr.lo] || !valid[pr.hi] {
			continue
		}
		lo, _ := limitByKey(pr.lo)
		hi, _ := limitByKey(pr.hi)
		if *lo.field(&l) > *hi.field(&l) {
			d.add(join(base, pr.lo), f.lineNode(pr.lo), "must not exceed %s (%d > %d)", pr.hi, *lo.field(&l), *hi.field(&l))
		}
	}
	return l
}
