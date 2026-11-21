package quality

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"localization-kit/src/internal/quality"
	"os"
)

func Characters(_ context.Context, args []string) error {
	if len(args) > 0 && args[0] == "warnings" {
		return characterWarnings(args[1:])
	}
	return fmt.Errorf("角色命令不支持该操作")
}

func characterWarnings(args []string) error {
	f := flag.NewFlagSet("characters warnings", flag.ContinueOnError)
	jobPath := f.String("job", "", "Existing job directory")
	cardsPath := f.String("cards", "", "Completed character material package")
	narrationPath := f.String("narration", "", "Optional JSON object mapping groups to third, first or unknown")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *jobPath == "" || *cardsPath == "" || f.NArg() != 0 {
		return fmt.Errorf("provide --job and --cards without positional arguments")
	}
	summary, err := quality.CharacterWarnings(*jobPath, *cardsPath, *narrationPath)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(summary)
}
