package cli

import (
	"flag"
	"fmt"
	"strings"
	"time"
)

// listFlag collects repeatable comma-separated string flags.
type listFlag []string

func (v *listFlag) String() string { return strings.Join(*v, ",") }

func (v *listFlag) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		item := strings.TrimSpace(part)
		if item != "" {
			*v = append(*v, item)
		}
	}
	return nil
}

// durationFlag binds a time.Duration flag value.
// Pointer Value + nil-safe String: flag.PrintDefaults reflects a zero
// Value and calls String() on it (target is nil in that probe).
type durationFlag struct {
	target *time.Duration
}

func (v *durationFlag) String() string {
	if v == nil || v.target == nil {
		return ""
	}
	return v.target.String()
}

func (v *durationFlag) Set(raw string) error {
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	*v.target = parsed
	return nil
}

func durationVar(fs *flag.FlagSet, target *time.Duration, name string, usage string) {
	fs.Var(&durationFlag{target: target}, name, usage+" (default "+target.String()+")")
}

// normalizeArgs moves positional arguments after flags so users can type
// `qda run words.txt -config qda.toml` in any order.
func normalizeArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if !isFlagToken(arg) {
			positionals = append(positionals, arg)
			continue
		}

		name := flagName(arg)
		registered := fs.Lookup(name)
		if registered == nil {
			flags = append(flags, arg)
			continue
		}

		flags = append(flags, arg)
		if strings.Contains(arg, "=") {
			continue
		}
		if isBoolFlag(registered) {
			if i+1 < len(args) && isBoolLiteral(args[i+1]) {
				i++
				flags[len(flags)-1] = arg + "=" + args[i]
			}
			continue
		}
		if i+1 >= len(args) {
			return nil, fmt.Errorf("flag needs an argument: -%s", name)
		}
		i++
		flags = append(flags, args[i])
	}

	return append(flags, positionals...), nil
}

func isFlagToken(value string) bool {
	return strings.HasPrefix(value, "-") && value != "-"
}

func flagName(value string) string {
	value = strings.TrimLeft(value, "-")
	if name, _, ok := strings.Cut(value, "="); ok {
		return name
	}
	return value
}

func isBoolFlag(f *flag.Flag) bool {
	boolFlag, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && boolFlag.IsBoolFlag()
}

func isBoolLiteral(value string) bool {
	switch strings.ToLower(value) {
	case "1", "0", "t", "f", "true", "false":
		return true
	default:
		return false
	}
}
