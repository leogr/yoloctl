package agentsession

import (
	"fmt"
	"slices"
	"strings"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
)

// Policy is a box's effective conversation policy.
type Policy string

const (
	// PolicyUnsupported: the profile declares no session block.
	PolicyUnsupported Policy = "unsupported"
	PolicyLast        Policy = agentbox.AgentSessionResumeLast
	PolicyNever       Policy = agentbox.AgentSessionResumeNever
	PolicyManual      Policy = agentbox.AgentSessionResumeManual
)

// Effective is a box's policy with how it was derived.
type Effective struct {
	Policy Policy
	// Declared is false when the manifest leaves the policy unset.
	Declared bool
	// LegacyArg is the identity argument in agent_extra_args that made an
	// unset policy manual.
	LegacyArg string
}

// Tracked reports whether yoloctl assigns, records and resumes conversations.
func (e Effective) Tracked() bool { return e.Policy == PolicyLast || e.Policy == PolicyNever }

// Describe is the policy as box show prints it.
func (e Effective) Describe() string {
	switch {
	case e.Policy == PolicyUnsupported:
		return "unsupported"
	case e.LegacyArg != "":
		return fmt.Sprintf("manual (legacy: agent_extra_args carries %s)", e.LegacyArg)
	case !e.Declared:
		return string(e.Policy) + " (default)"
	}
	return string(e.Policy)
}

// EffectivePolicy derives a box's policy. An unset policy is manual when
// agent_extra_args carry an identity argument, so existing launches never
// change; an explicit last or never with such an argument is an error that
// names it and both ways out.
func EffectivePolicy(spec *agentprofile.SessionSpec, cfg *agentbox.Config) (Effective, error) {
	declared := cfg.ResumePolicy()
	if spec == nil {
		return Effective{Policy: PolicyUnsupported, Declared: declared != ""}, nil
	}
	arg := ReservedArg(spec, cfg.AgentExtraArgs)
	if !spec.Resumes() {
		// Observe-only: every start is a new conversation, tracked only to
		// read its reports.
		switch declared {
		case "":
			if arg != "" {
				return Effective{Policy: PolicyManual, LegacyArg: arg}, nil
			}
			return Effective{Policy: PolicyNever}, nil
		case agentbox.AgentSessionResumeLast:
			return Effective{}, fmt.Errorf("agent %s cannot resume a conversation; set --agent-session-resume never or manual", cfg.Agent)
		}
	}
	switch declared {
	case "":
		if arg != "" {
			return Effective{Policy: PolicyManual, LegacyArg: arg}, nil
		}
		return Effective{Policy: PolicyLast}, nil
	case agentbox.AgentSessionResumeManual:
		return Effective{Policy: PolicyManual, Declared: true}, nil
	case agentbox.AgentSessionResumeLast, agentbox.AgentSessionResumeNever:
		if arg != "" {
			return Effective{}, fmt.Errorf("agent_extra_args uses %s, which takes over the conversation, while agent_session.resume is %s; set --agent-session-resume manual or drop %s", arg, declared, arg)
		}
		return Effective{Policy: Policy(declared), Declared: true}, nil
	}
	return Effective{}, fmt.Errorf("agent_session.resume %q must be last, never or manual", declared)
}

// ReservedArg returns the first argument of args that the profile reserves
// for conversation identity, matching the exact flag or its --flag=value
// form, or "" when there is none.
func ReservedArg(spec *agentprofile.SessionSpec, args []string) string {
	if spec == nil {
		return ""
	}
	for _, a := range args {
		for _, r := range spec.ReservedArgs {
			if a == r || (strings.HasPrefix(r, "--") && strings.HasPrefix(a, r+"=")) {
				return r
			}
		}
	}
	return ""
}

// Argv builds the agent command line, before launch_env, for a launch of
// kind with conversation id: command, default_args and the assign arguments
// for a new conversation, resume_argv and default_args for the recorded one,
// or command and default_args for a learn launch, which takes no id;
// agent_extra_args always follow.
func Argv(profile *agentprofile.Profile, kind, id string, extra []string) ([]string, error) {
	spec := profile.Session
	if spec == nil {
		return nil, fmt.Errorf("agent %s declares no conversation identity", profile.Name)
	}
	if kind == KindLearn {
		if id != "" {
			return nil, fmt.Errorf("a learn launch of agent %s takes no conversation id", profile.Name)
		}
		argv := append([]string{profile.Command}, profile.DefaultArgs...)
		return append(argv, extra...), nil
	}
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid conversation id %q", id)
	}
	expand := func(args []string) []string {
		out := make([]string, 0, len(args))
		for _, a := range args {
			a = strings.ReplaceAll(a, "{{session_id}}", id)
			out = append(out, strings.ReplaceAll(a, "{{command}}", profile.Command))
		}
		return out
	}
	var argv []string
	switch kind {
	case KindAssign:
		if len(spec.AssignArgs) == 0 {
			return nil, fmt.Errorf("agent %s cannot start a conversation under a chosen id", profile.Name)
		}
		argv = append([]string{profile.Command}, profile.DefaultArgs...)
		argv = append(argv, expand(spec.AssignArgs)...)
	case KindResume:
		argv = append(expand(spec.ResumeArgv), profile.DefaultArgs...)
	default:
		return nil, fmt.Errorf("unknown launch kind %q", kind)
	}
	return append(argv, extra...), nil
}

// Carried is what a resume of one conversation may carry from its last
// observation.
type Carried struct{ Effort, Model string }

// Carried returns the effort and model last reported by conversation id
// itself: a value set by a report naming another conversation, or none, is
// never carried, whatever its time.
func (r *Record) Carried(id string) Carried {
	if r == nil || r.Observed == nil || id == "" {
		return Carried{}
	}
	own := func(v *ObservedValue) string {
		if v == nil || v.Session != id {
			return ""
		}
		return v.Value
	}
	return Carried{Effort: own(r.Observed.Effort), Model: own(r.Observed.Model)}
}

// CarryArgs are the profile's resume_carry arguments for carried values:
// the effort only when the profile lists it, the model only when it is a
// plain model id (letters, digits, '.', '_', ':', '/', '@', '[', ']' and
// inner '-'), so no reported text can become another option. Effort comes
// first. Nil when the profile carries nothing that applies.
func CarryArgs(profile *agentprofile.Profile, carried Carried) []string {
	if profile == nil || profile.Session == nil || profile.Session.ResumeCarry == nil {
		return nil
	}
	c := profile.Session.ResumeCarry
	var out []string
	expand := func(args []string, placeholder, value string) {
		for _, a := range args {
			out = append(out, strings.ReplaceAll(a, placeholder, value))
		}
	}
	if e := c.Effort; e != nil && carried.Effort != "" && slices.Contains(e.Values, carried.Effort) {
		expand(e.Args, "{{effort}}", carried.Effort)
	}
	if mdl := c.Model; mdl != nil && plainModelID(carried.Model) {
		expand(mdl.Args, "{{model}}", carried.Model)
	}
	return out
}

func plainModelID(s string) bool {
	if s == "" || len(s) > 128 || s[0] == '-' {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("._:/@[]-", r):
		default:
			return false
		}
	}
	return true
}
