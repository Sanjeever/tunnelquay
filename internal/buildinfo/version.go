package buildinfo

import (
	"flag"
	"fmt"
)

var Version = "dev"
var Commit = "unknown"

func ConfigureHelp(program string) {
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintln(out, "用法：", program, "[参数]")
		flag.VisitAll(func(f *flag.Flag) {
			value := " <值>"
			if getter, ok := f.Value.(flag.Getter); ok {
				if _, isBool := getter.Get().(bool); isBool {
					value = ""
				}
			}
			fmt.Fprintf(out, "  -%s%s\n    %s", f.Name, value, f.Usage)
			if f.DefValue != "" && f.DefValue != "false" {
				fmt.Fprintf(out, "（默认 %s）", f.DefValue)
			}
			fmt.Fprintln(out)
		})
	}
}
