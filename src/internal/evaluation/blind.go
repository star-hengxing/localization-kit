package evaluation

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"localization-kit/src/internal/fileutil"
	"localization-kit/src/internal/jsonfile"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

type BlindItem struct {
	Token   string   `json:"token"`
	Src     string   `json:"src"`
	A       string   `json:"a"`
	B       string   `json:"b"`
	Context []string `json:"context,omitempty"`
}

type BlindBatch struct {
	Version int         `json:"version"`
	Batch   string      `json:"batch"`
	Items   []BlindItem `json:"items"`
	Hash    string      `json:"hash"`
}

type BlindMapping struct {
	Pair      string `json:"pair"`
	Candidate string `json:"candidate"`
	ID        string `json:"id"`
	Group     string `json:"group"`
}

type BlindKey struct {
	Version      int                     `json:"version"`
	Batches      map[string]string       `json:"batches"`
	Items        map[string]BlindMapping `json:"items"`
	Pairs        int                     `json:"pairs"`
	SourceHash   string                  `json:"source_hash"`
	BaselineJob  string                  `json:"baseline_job"`
	CandidateJob string                  `json:"candidate_job"`
	Hash         string                  `json:"hash"`
}

type BlindVerdict struct {
	Token  string `json:"token"`
	Winner string `json:"winner"`
	Reason string `json:"reason,omitempty"`
}

type BlindReport struct {
	BatchHash string         `json:"batch_hash"`
	Items     []BlindVerdict `json:"items"`
}

func blindHash(batch BlindBatch) (string, error) {
	batch.Hash = ""
	return jsonfile.Digest(batch)
}

func blindKeyHash(key BlindKey) (string, error) {
	key.Hash = ""
	return jsonfile.Digest(key)
}

func randomToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func shuffleBlind(items []BlindItem) error {
	for i := len(items) - 1; i > 0; i-- {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return err
		}
		j := int(n.Int64())
		items[i], items[j] = items[j], items[i]
	}
	return nil
}

func PrepareBlind(inputPath, outputPath string, size int) (summary map[string]any, resultErr error) {
	if inputPath == "" || outputPath == "" || size < 1 {
		return nil, fmt.Errorf("invalid blind preparation options")
	}
	input, out, batchSize := &inputPath, &outputPath, &size
	run := func() error {

		var comparison Comparison
		if err := jsonfile.Read(*input, &comparison); err != nil {
			return err
		}
		if comparison.Version != 1 || comparison.BaselineJob == "" || comparison.CandidateJob == "" {
			return fmt.Errorf("无效的作业比较报告")
		}
		outAbs, err := fileutil.Canonical(*out)
		if err != nil {
			return err
		}
		for _, path := range []string{*input, comparison.BaselineJob, comparison.CandidateJob} {
			abs, err := fileutil.Canonical(path)
			if err != nil {
				return err
			}
			if fileutil.Overlaps(outAbs, abs) {
				return fmt.Errorf("评测目录不能与输入报告或作业重叠")
			}
		}
		sourceHash, err := jsonfile.Digest(comparison)
		if err != nil {
			return err
		}
		key := BlindKey{Version: 1, Batches: map[string]string{}, Items: map[string]BlindMapping{}, SourceHash: sourceHash, BaselineJob: comparison.BaselineJob, CandidateJob: comparison.CandidateJob}
		passes := [2][]BlindItem{}
		seen := map[string]bool{}
		for index, item := range comparison.Items {
			if item.ID == "" || seen[item.ID] {
				return fmt.Errorf("比较报告含空或重复的条目 ID")
			}
			seen[item.ID] = true
			eligible := item.Baseline.Status == "done" && item.Candidate.Status == "done" && item.Baseline.Dst != nil && item.Candidate.Dst != nil && !item.Baseline.NeedsReview && !item.Candidate.NeedsReview && !item.Baseline.Passthrough && !item.Candidate.Passthrough
			if !eligible {
				if item.Equal != nil {
					return fmt.Errorf("不可配对条目的比较结果无效: %s", item.ID)
				}
				continue
			}
			equal := *item.Baseline.Dst == *item.Candidate.Dst
			if item.Equal == nil || *item.Equal != equal {
				return fmt.Errorf("条目的比较结果与译文不一致: %s", item.ID)
			}
			if equal {
				continue
			}
			var context []string
			start := index
			for start > 0 && start > index-3 && comparison.Items[start-1].Group == item.Group {
				start--
			}
			for i := start; i < index; i++ {
				context = append(context, comparison.Items[i].Src)
			}
			pair, err := randomToken()
			if err != nil {
				return err
			}
			key.Pairs++
			firstPosition, err := rand.Int(rand.Reader, big.NewInt(2))
			if err != nil {
				return err
			}
			for pass := 0; pass < 2; pass++ {
				token, err := randomToken()
				if err != nil {
					return err
				}
				row := BlindItem{Token: token, Src: item.Src, A: *item.Baseline.Dst, B: *item.Candidate.Dst, Context: context}
				position := "b"
				if int(firstPosition.Int64()) == pass {
					row.A, row.B = row.B, row.A
					position = "a"
				}
				passes[pass] = append(passes[pass], row)
				key.Items[token] = BlindMapping{Pair: pair, Candidate: position, ID: item.ID, Group: item.Group}
			}
		}
		if key.Pairs == 0 {
			return fmt.Errorf("没有可供盲评的不同译文")
		}
		var batches []BlindBatch
		for pass := range passes {
			if err := shuffleBlind(passes[pass]); err != nil {
				return err
			}
			for start := 0; start < len(passes[pass]); start += *batchSize {
				end := min(start+*batchSize, len(passes[pass]))
				batch := BlindBatch{Version: 1, Batch: fmt.Sprintf("%d-%04d", pass+1, start/(*batchSize)+1), Items: passes[pass][start:end]}
				batch.Hash, err = blindHash(batch)
				if err != nil {
					return err
				}
				key.Batches[batch.Batch] = batch.Hash
				batches = append(batches, batch)
			}
		}
		key.Hash, err = blindKeyHash(key)
		if err != nil {
			return err
		}
		if err := fileutil.CheckOutput(*out); err != nil {
			return err
		}
		stage, err := os.MkdirTemp(filepath.Dir(*out), ".localization-blind-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(stage)
		batchDir := filepath.Join(stage, "batches")
		if err := os.Mkdir(batchDir, 0700); err != nil {
			return fmt.Errorf("创建盲评目录失败，可能需要清理 %s: %w", *out, err)
		}
		if err := jsonfile.Create(filepath.Join(stage, "key.json"), key); err != nil {
			return fmt.Errorf("写入盲评密钥失败，可能需要清理 %s: %w", *out, err)
		}
		for _, batch := range batches {
			if err := jsonfile.Create(filepath.Join(batchDir, batch.Batch+".json"), batch); err != nil {
				return fmt.Errorf("写入盲评批次失败，可能需要清理 %s: %w", *out, err)
			}
		}
		if err := fileutil.PublishDirectory(stage, *out); err != nil {
			return fmt.Errorf("发布盲评目录: %w", err)
		}
		summary = map[string]any{"pairs": key.Pairs, "batches": len(batches), "directory": *out}
		return nil

	}
	resultErr = run()
	return summary, resultErr
}

func ScoreBlind(dirPath, reportsPath, outputPath string) (summary map[string]any, resultErr error) {
	if dirPath == "" || reportsPath == "" || outputPath == "" {
		return nil, fmt.Errorf("invalid blind scoring options")
	}
	dir, reports, out := &dirPath, &reportsPath, &outputPath
	run := func() error {

		outputAbs, err := fileutil.Canonical(*out)
		if err != nil {
			return err
		}
		for _, path := range []string{*dir, *reports} {
			abs, err := fileutil.Canonical(path)
			if err != nil {
				return err
			}
			if fileutil.Overlaps(abs, outputAbs) {
				return fmt.Errorf("评分报告必须位于盲评和评审结果目录之外")
			}
		}
		var key BlindKey
		if err := jsonfile.Read(filepath.Join(*dir, "key.json"), &key); err != nil {
			return err
		}
		keyHash, err := blindKeyHash(key)
		if err != nil {
			return err
		}
		if key.Version != 1 || key.Pairs < 1 || len(key.Batches) == 0 || len(key.Items) != key.Pairs*2 || key.SourceHash == "" || key.BaselineJob == "" || key.CandidateJob == "" || key.Hash == "" || key.Hash != keyHash {
			return fmt.Errorf("盲评映射无效")
		}
		if err := fileutil.CheckNewPath(*out, key.BaselineJob, key.CandidateJob); err != nil {
			return err
		}
		names := make([]string, 0, len(key.Batches))
		for name := range key.Batches {
			if filepath.Base(name) != name || name == "." || name == ".." {
				return fmt.Errorf("无效批次名")
			}
			names = append(names, name)
		}
		sort.Strings(names)
		pairScores := map[string][]float64{}
		pairRefused := map[string]bool{}
		pairBatches := map[string][]string{}
		pairRows := map[string][]BlindItem{}
		seenTokens := map[string]bool{}
		missing := []string{}
		for _, name := range names {
			var batch BlindBatch
			if err := jsonfile.Read(filepath.Join(*dir, "batches", name+".json"), &batch); err != nil {
				return err
			}
			batchHash, err := blindHash(batch)
			if err != nil {
				return err
			}
			if batch.Version != 1 || batch.Batch != name || batch.Hash != key.Batches[name] || batch.Hash != batchHash {
				return fmt.Errorf("盲评批次已更改: %s", name)
			}
			expected := map[string]bool{}
			for _, item := range batch.Items {
				mapping, ok := key.Items[item.Token]
				if !ok || seenTokens[item.Token] || mapping.Pair == "" || (mapping.Candidate != "a" && mapping.Candidate != "b") {
					return fmt.Errorf("无效或重复的盲评条目: %s", item.Token)
				}
				seenTokens[item.Token] = true
				expected[item.Token] = true
				pairBatches[mapping.Pair] = append(pairBatches[mapping.Pair], name)
				pairRows[mapping.Pair] = append(pairRows[mapping.Pair], item)
			}
			var report BlindReport
			path := filepath.Join(*reports, name+".json")
			if err := jsonfile.Read(path, &report); err != nil {
				if os.IsNotExist(err) {
					missing = append(missing, name)
					continue
				}
				return err
			}
			if report.BatchHash != batch.Hash || len(report.Items) != len(expected) {
				return fmt.Errorf("评审结果与批次不匹配: %s", name)
			}
			for _, item := range report.Items {
				if !expected[item.Token] {
					return fmt.Errorf("未知或重复的评审条目: %s", item.Token)
				}
				delete(expected, item.Token)
				mapping := key.Items[item.Token]
				switch item.Winner {
				case "a", "b":
					score := 0.0
					if item.Winner == mapping.Candidate {
						score = 1
					}
					pairScores[mapping.Pair] = append(pairScores[mapping.Pair], score)
				case "tie":
					pairScores[mapping.Pair] = append(pairScores[mapping.Pair], 0.5)
				case "refused":
					pairRefused[mapping.Pair] = true
				default:
					return fmt.Errorf("无效的评审结论: %s", item.Token)
				}
			}
		}
		if len(seenTokens) != len(key.Items) {
			return fmt.Errorf("盲评映射包含未使用的条目")
		}
		complete, refused, single, pending := 0, 0, 0, 0
		total := 0.0
		var outcomes []map[string]any
		pairs := map[string]bool{}
		pairPositions := map[string]map[string]bool{}
		pairItems := map[string]BlindMapping{}
		sourcePairs := map[string]string{}
		for _, item := range key.Items {
			pairs[item.Pair] = true
			if pairPositions[item.Pair] == nil {
				pairPositions[item.Pair] = map[string]bool{}
				pairItems[item.Pair] = item
			}
			previous := pairItems[item.Pair]
			if other, ok := sourcePairs[item.ID]; ok && other != item.Pair {
				return fmt.Errorf("盲评原文条目重复配对")
			}
			sourcePairs[item.ID] = item.Pair
			if item.ID == "" || item.ID != previous.ID || item.Group != previous.Group || pairPositions[item.Pair][item.Candidate] {
				return fmt.Errorf("盲评配对映射无效")
			}
			pairPositions[item.Pair][item.Candidate] = true
		}
		if len(pairs) != key.Pairs {
			return fmt.Errorf("盲评配对数不匹配")
		}
		for pair := range pairs {
			if len(pairPositions[pair]) != 2 {
				return fmt.Errorf("盲评配对位置不完整")
			}
			rows, batches := pairRows[pair], pairBatches[pair]
			if len(rows) != 2 || len(batches) != 2 || !strings.HasPrefix(batches[0], "1-") || !strings.HasPrefix(batches[1], "2-") || rows[0].Src != rows[1].Src || rows[0].A != rows[1].B || rows[0].B != rows[1].A || !reflect.DeepEqual(rows[0].Context, rows[1].Context) {
				return fmt.Errorf("盲评配对内容或批次不一致")
			}
			scores := pairScores[pair]
			outcome := map[string]any{"id": pairItems[pair].ID, "group": pairItems[pair].Group}
			switch {
			case pairRefused[pair]:
				refused++
				outcome["status"] = "refused"
			case len(scores) == 2:
				complete++
				score := (scores[0] + scores[1]) / 2
				total += score
				outcome["status"] = "scored"
				outcome["candidate_score"] = score
				outcome["inconsistent"] = scores[0] != scores[1]
			case len(scores) == 1:
				single++
				outcome["status"] = "single_pass"
			case len(scores) == 0:
				pending++
				outcome["status"] = "pending"
			default:
				return fmt.Errorf("盲评配对条目数量无效")
			}
			outcomes = append(outcomes, outcome)
		}
		sort.Slice(outcomes, func(i, j int) bool { return outcomes[i]["id"].(string) < outcomes[j]["id"].(string) })
		var rate *float64
		if complete > 0 {
			value := total / float64(complete)
			rate = &value
		}
		result := map[string]any{"baseline_job": key.BaselineJob, "candidate_job": key.CandidateJob, "source_hash": key.SourceHash, "pairs": key.Pairs, "complete": len(missing) == 0, "scored_pairs": complete, "single_pass": single, "pending": pending, "refused": refused, "candidate_score_rate": rate, "missing_batches": missing, "items": outcomes}
		if err := jsonfile.Create(*out, result); err != nil {
			return err
		}
		summary = map[string]any{"complete": len(missing) == 0, "scored_pairs": complete, "single_pass": single, "pending": pending, "refused": refused, "candidate_score_rate": rate, "missing_batches": missing, "report": *out}
		return nil
	}
	resultErr = run()
	return summary, resultErr
}
