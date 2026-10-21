// Package pulseguard refuses network targets an agent must not touch.
//
// It is a separate package because it only means something for agents that
// reach out over the network on the server's instruction. A build agent does
// not need it and should not have to compile it in.
//
// Why this belongs in the framework at all: when a server-side rule is
// misconfigured, this is the last line of defence. An agent inside a private
// network especially needs it — one bad rule should not turn it into a scanner
// of its own subnet. That argument holds for any system where a server tells
// remote agents which addresses to contact, so it is not an application
// concern.
package pulseguard

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path"
	"strings"
)

// Policy is the deny configuration. The zero value denies nothing except
// loopback and the addresses that are never legitimate targets.
type Policy struct {
	// DenyCIDRs are address ranges that must never be contacted.
	DenyCIDRs []string `json:"deny_cidrs,omitempty"`
	// DenyDomains are glob patterns matched against the hostname.
	DenyDomains []string `json:"deny_domains,omitempty"`
}

// Options configures New.
type Options struct {
	// Local is this agent's own policy, from its configuration file. Pushed
	// entries are added to it and never replace it.
	Local Policy
	// AllowLoopback permits 127.0.0.0/8 and ::1.
	//
	// There is deliberately no way to set this from a server push: it is the
	// machine owner's decision, and a compromised or mistaken server must not
	// be able to turn an agent loose on its own host.
	AllowLoopback bool
	// Logger records why targets were refused. Nil discards.
	Logger *slog.Logger
}

// Guard decides whether a target may be contacted.
type Guard struct {
	denyNets    []*net.IPNet
	denyDomains []string
	allowLoop   bool
	log         *slog.Logger
}

// ErrDenied is the error class every refusal wraps, so a caller can tell a
// policy refusal from a malformed input.
var ErrDenied = errors.New("pulseguard: target denied")

// New compiles a policy into a Guard. Unparseable entries are an error: a deny
// rule that silently did not compile is worse than a startup failure.
func New(o Options) (*Guard, error) {
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	g := &Guard{allowLoop: o.AllowLoopback, log: log}
	for _, c := range o.Local.DenyCIDRs {
		_, n, err := net.ParseCIDR(strings.TrimSpace(c))
		if err != nil {
			return nil, fmt.Errorf("pulseguard: deny cidr %q: %w", c, err)
		}
		g.denyNets = append(g.denyNets, n)
	}
	for _, d := range o.Local.DenyDomains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			return nil, errors.New("pulseguard: empty deny domain")
		}
		g.denyDomains = append(g.denyDomains, d)
	}
	return g, nil
}

// CheckTarget vets a hostname and port before the job is accepted.
//
// It cannot enforce address-based rules — the name has not been resolved yet —
// so an Executor must also call CheckIPs once it knows the addresses.
func (g *Guard) CheckTarget(host string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%w: port %d is out of range", ErrDenied, port)
	}
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return fmt.Errorf("%w: empty host", ErrDenied)
	}
	for _, pat := range g.denyDomains {
		if ok, _ := path.Match(pat, h); ok {
			g.log.Info("target denied by domain rule", "host", h, "pattern", pat)
			return fmt.Errorf("%w: %s matches deny pattern %s", ErrDenied, h, pat)
		}
	}
	// A literal address in the host position can be checked right now.
	if ip := net.ParseIP(h); ip != nil {
		return g.checkIP(ip)
	}
	return nil
}

// CheckIPs vets resolved addresses. An Executor MUST call this after
// resolution: address rules are the half of the policy that a hostname check
// cannot enforce, and the framework never sees the resolved addresses.
func (g *Guard) CheckIPs(ips []net.IP) error {
	for _, ip := range ips {
		if err := g.checkIP(ip); err != nil {
			return err
		}
	}
	return nil
}

func (g *Guard) checkIP(ip net.IP) error {
	if !g.allowLoop && ip.IsLoopback() {
		g.log.Info("target denied: loopback", "ip", ip.String())
		return fmt.Errorf("%w: %s is loopback", ErrDenied, ip)
	}
	// Never legitimate targets, regardless of configuration: unspecified
	// (0.0.0.0, ::) and link-local multicast. These are not deny-list entries
	// an operator should have to remember.
	if ip.IsUnspecified() {
		return fmt.Errorf("%w: %s is unspecified", ErrDenied, ip)
	}
	if ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return fmt.Errorf("%w: %s is multicast", ErrDenied, ip)
	}
	for _, n := range g.denyNets {
		if n.Contains(ip) {
			g.log.Info("target denied by cidr rule", "ip", ip.String(), "cidr", n.String())
			return fmt.Errorf("%w: %s is in denied range %s", ErrDenied, ip, n)
		}
	}
	return nil
}

// payloadKey is where pulseguard's settings live inside AgentConfig.payload. The
// payload is conventionally a JSON object keyed by subsystem, so the guard and
// the application's own configuration can coexist without either knowing about
// the other.
const payloadKey = "pulseguard"

// Merge builds a Guard from local policy plus pushed entries.
//
// The deny list only ever grows: pushed entries are ADDED to the local ones,
// which are never removed. A compromised or mistaken server must not be able
// to widen where an agent may go — which is why a console should present these
// as "additionally forbidden" rather than as the agent's deny list.
func Merge(o Options, pushed Policy) (*Guard, error) {
	merged := Options{
		AllowLoopback: o.AllowLoopback, // never pushable; see Options
		Logger:        o.Logger,
		Local: Policy{
			DenyCIDRs:   append(append([]string{}, o.Local.DenyCIDRs...), pushed.DenyCIDRs...),
			DenyDomains: append(append([]string{}, o.Local.DenyDomains...), pushed.DenyDomains...),
		},
	}
	return New(merged)
}

// ParsePayload extracts pulseguard's section from an AgentConfig payload. An
// absent section is not an error: it means the server pushed nothing extra.
func ParsePayload(payload []byte) (Policy, error) {
	if len(payload) == 0 {
		return Policy{}, nil
	}
	var sections map[string]json.RawMessage
	if err := json.Unmarshal(payload, &sections); err != nil {
		return Policy{}, fmt.Errorf("pulseguard: config payload is not a JSON object: %w", err)
	}
	raw, ok := sections[payloadKey]
	if !ok {
		return Policy{}, nil
	}
	var p Policy
	if err := json.Unmarshal(raw, &p); err != nil {
		return Policy{}, fmt.Errorf("pulseguard: %s section: %w", payloadKey, err)
	}
	return p, nil
}

// EncodePayload renders a policy as the pulseguard section of a config
// payload, merging into any sections already present. Server-side helper.
func EncodePayload(existing []byte, p Policy) ([]byte, error) {
	sections := map[string]json.RawMessage{}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &sections); err != nil {
			return nil, fmt.Errorf("pulseguard: existing payload is not a JSON object: %w", err)
		}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	sections[payloadKey] = raw
	return json.Marshal(sections)
}
