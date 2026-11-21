package quality

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"localization-kit/src/internal/cli/common"
	"localization-kit/src/internal/config"
	"localization-kit/src/internal/quality"
	"os"
)

func Review(ctx context.Context, args []string, configPath string, cfg config.Config) error {
	if len(args) == 0 {
		return fmt.Errorf("review requires prepare, run or validate")
	}
	switch args[0] {
	case "run":
		return runReviewModel(ctx, args[1:], configPath, cfg)
	case "prepare":
		return prepareReview(args[1:])
	case "validate":
		flags := flag.NewFlagSet("review validate", flag.ContinueOnError)
		jobPath := flags.String("job", "", "Job directory")
		input := flags.String("input", "", "Review material JSON")
		reportPath := flags.String("report", "", "Review report JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *jobPath == "" || *input == "" || *reportPath == "" || len(flags.Args()) != 0 {
			return fmt.Errorf("--job, --input and --report are required")
		}
		summary, err := quality.ValidateReviewFiles(*jobPath, *input, *reportPath)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(summary)

	default:
		return fmt.Errorf("unknown review command %q", args[0])
	}
}

func prepareReview(args []string) error {
	flags := flag.NewFlagSet("review prepare", flag.ContinueOnError)
	jobPath := flags.String("job", "", "Job directory")
	kind := flags.String("kind", "fidelity", "fidelity or subject")
	out := flags.String("out", "", "New material JSON file")
	batchSize := flags.Int("batch-size", 20, "Targets per batch")
	cardsPath := flags.String("cards", "", "Character cards JSON")
	candidatePath := flags.String("candidate", "", "Candidate translations JSON")
	var glossaries, ids, groups common.StringList
	flags.Var(&glossaries, "glossary", "Glossary JSON; repeatable")
	flags.Var(&ids, "id", "Target ID; repeatable")
	flags.Var(&groups, "group", "Target group; repeatable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *jobPath == "" || *out == "" || len(flags.Args()) != 0 || *batchSize < 1 || (*kind != "fidelity" && *kind != "subject") {
		return fmt.Errorf("invalid review prepare arguments")
	}
	if *candidatePath != "" && (len(ids) > 0 || len(groups) > 0) {
		return fmt.Errorf("--candidate 不能与 --id 或 --group 同时使用")
	}
	result, err := quality.PrepareReview(quality.ReviewOptions{JobPath: *jobPath, Kind: *kind, Output: *out, CardsPath: *cardsPath, CandidatePath: *candidatePath, BatchSize: *batchSize, Glossaries: glossaries, IDs: ids, Groups: groups})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
