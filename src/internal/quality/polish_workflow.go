package quality

import (
	"fmt"
	"localization-kit/src/internal/fileutil"
	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/store"
)

type PolishOptions struct {
	JobPath, Input, ReportPath, Output, Mode string
	AdditionalInputs, AdditionalReports      []string
}

func PreparePolish(options PolishOptions) error {
	if options.JobPath == "" || options.Input == "" || options.Output == "" || (options.Mode != "fix" && options.Mode != "rewrite") || len(options.AdditionalInputs) != len(options.AdditionalReports) {
		return fmt.Errorf("invalid polish options")
	}
	jobPath, input, reportPath, output, mode := &options.JobPath, &options.Input, &options.ReportPath, &options.Output, &options.Mode
	additionalInputs, additionalReports := options.AdditionalInputs, options.AdditionalReports

	jobAbs, err := fileutil.Canonical(*jobPath)
	if err != nil {
		return err
	}
	outAbs, err := fileutil.Canonical(*output)
	if err != nil {
		return err
	}
	if fileutil.Overlaps(jobAbs, outAbs) {
		return fmt.Errorf("润色材料不能放在作业目录内")
	}
	reviewMaterial, err := ReadReviewMaterial(*input)
	if err != nil {
		return err
	}
	if reviewMaterial.CandidateHash != "" {
		return fmt.Errorf("润色底稿不能使用候选译文的审查材料")
	}
	job, err := store.OpenReadOnly(*jobPath)
	if err != nil {
		return err
	}
	defer job.Close()
	material := PolishMaterial{Version: 1, Mode: *mode, Review: reviewMaterial}
	if *mode == "fix" {
		material.Instruction = "逐条对照原文纠正误译、漏译、增译并改善中文，保持含义和语气。保留占位符与每条结构。"
	} else {
		material.Instruction = "逐条对照原文重写成自然中文，保持含义、语气和人物关系；不得增删信息，保留占位符与每条结构。"
	}
	material.Instruction += " 综合所有检查意见并自行判断冲突。返回 JSON：material_hash、actor、items；每项含 id、expected_result_hash、dst、reason。所有目标必须覆盖，改动应说明理由；上下文只供参考。"
	if *reportPath != "" {
		report, err := ReadReviewReport(*reportPath)
		if err != nil {
			return err
		}
		if err := ValidateReview(job, reviewMaterial, report); err != nil {
			return err
		}
		material.Report = &report
	}
	for i, path := range additionalInputs {
		reviewMaterial, err := ReadReviewMaterial(path)
		if err != nil {
			return err
		}
		report, err := ReadReviewReport(additionalReports[i])
		if err != nil {
			return err
		}
		material.Additional = append(material.Additional, PolishReview{Material: reviewMaterial, Report: report})
	}
	if _, err := PolishTargets(job, material); err != nil {
		return err
	}
	material.Hash, err = PolishDigest(material)
	if err != nil {
		return err
	}
	return jsonfile.Create(*output, material)

}
