package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"maps"
	"slices"

	"localization-kit/src/internal/document"
	"localization-kit/src/internal/jsonfile"
	"localization-kit/src/internal/quality"
	"localization-kit/src/internal/store"
	"localization-kit/src/internal/translate"
	"localization-kit/src/internal/translate/rules"
)

func scopeName(scope string) string {
	if scope == "" {
		return "group"
	}
	return scope
}

func stageComplete(deps dependencies, segment document.Segment, current document.Result, stage string, config stageConfig) bool {
	result := deps.job.Done[segment.ID]
	if current.NeedsReview && !result.NeedsReview && result.HistoryUsed && result.HistoryHash != translate.HistoryDigest(deps.job, segment) {
		return false
	}
	completion := result.PolishCompletion
	if stage == "proofread" {
		completion = result.ProofreadCompletion
	}
	if result.NeedsReview || completion == nil || completion.Model != config.Model || completion.Revision != config.Revision || scopeName(completion.Scope) != scopeName(config.Scope) {
		return false
	}
	digest, err := scopedDigest(deps, segment, result, stage, config.Model, config.Revision, config.Context, config.MaxLines, scopeName(config.Scope), completion.BlockStart, completion.BlockEnd)
	return err == nil && digest != "" && completion.Digest == digest
}

func materials(glossaries []string, cardsPath string, segments []document.Segment) ([]rules.Entry, json.RawMessage, error) {
	entries, err := rules.LoadGlossaries(glossaries)
	if err != nil {
		return nil, nil, err
	}
	if cardsPath == "" {
		return entries, nil, nil
	}
	cards, err := quality.LoadCharacterCards(cardsPath, segments)
	if err != nil {
		return nil, nil, err
	}
	data, err := json.Marshal(cards)
	if err != nil {
		return nil, nil, err
	}
	return entries, data, nil
}

type dependencies struct {
	terms, source, preceding, translations map[string]string
	cards                                  json.RawMessage
	job                                    *store.Store
	reviews                                map[string][]ReviewNote
	reviewsHash                            string
}

func polishDraft(job *store.Store) *store.Store {
	var draft *store.Store
	var saved map[string]document.Result
	for id, current := range job.Done {
		if current.PolishCompletion == nil || (current.Origin != "proofread" && current.Stage != "proofread" && !current.ContextReview) {
			continue
		}
		if saved == nil {
			saved, _ = job.StageResults("polish")
		}
		prior := saved[id]
		if prior.PolishCompletion == nil || prior.PolishCompletion.TaskID != current.PolishCompletion.TaskID || prior.Signature != current.Signature {
			continue
		}
		if current.NeedsReview && (!current.ContextReview || current.ReviewSignature != prior.Signature || current.ReviewTermsHash != prior.TermsHash) {
			continue
		}
		if draft == nil {
			copy := *job
			copy.Done = maps.Clone(job.Done)
			draft = &copy
		}
		// A later edit can change the live text while the accepted polish remains valid.
		draft.Done[id] = prior
	}
	if draft != nil {
		return draft
	}
	return job
}

func stageDependencies(job *store.Store, stage string, entries []rules.Entry, cards json.RawMessage, reviews []ReviewFiles) (dependencies, error) {
	if stage == "polish" {
		job = polishDraft(job)
	}
	deps, err := dependencyHashes(job, entries, cards)
	if err != nil {
		return dependencies{}, err
	}
	deps.reviews, deps.reviewsHash, err = loadReviews(job, reviews, entries, cards)
	return deps, err
}

func dependencyHashes(job *store.Store, entries []rules.Entry, cards json.RawMessage) (dependencies, error) {
	value := dependencies{terms: translate.TermsHashes(job.Segments, entries), source: map[string]string{}, preceding: map[string]string{}, translations: map[string]string{}, cards: cards, job: job}
	sources, preceding := map[string]hash.Hash{}, map[string]hash.Hash{}
	for _, segment := range job.Segments {
		if sources[segment.Group] == nil {
			sources[segment.Group] = sha256.New()
			preceding[segment.Group] = sha256.New()
		}
		data, err := json.Marshal(segment)
		if err != nil {
			return dependencies{}, err
		}
		sources[segment.Group].Write(append(data, '\n'))
		value.preceding[segment.ID] = hex.EncodeToString(preceding[segment.Group].Sum(nil))
		if result, ok := job.Done[segment.ID]; ok {
			data, err := json.Marshal(Line{ID: segment.ID, Src: segment.Src, Dst: result.Dst})
			if err != nil {
				return dependencies{}, err
			}
			preceding[segment.Group].Write(append(data, '\n'))
		}
	}
	for group, h := range sources {
		value.source[group] = hex.EncodeToString(h.Sum(nil))
		value.translations[group] = hex.EncodeToString(preceding[group].Sum(nil))
	}
	return value, nil
}

func taskBasis(deps dependencies, segment document.Segment, result document.Result, stage, model, revision string, context, maxLines int) (string, error) {
	value := struct {
		Stage, Model, Revision, Signature, Terms, Simplified, Source string
		Cards                                                        json.RawMessage
		Context, MaxLines                                            int
	}{stage, model, revision, result.Signature, deps.terms[segment.Group], rules.SimplifiedDigest(), deps.source[segment.Group], deps.cards, context, maxLines}
	basis, err := jsonfile.Digest(value)
	if err != nil {
		return "", err
	}
	if deps.reviewsHash != "" {
		reviewsHash, err := jsonfile.Digest(deps.reviews[segment.ID])
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256([]byte(basis + "\x00" + deps.reviewsHash + "\x00" + reviewsHash))
		basis = hex.EncodeToString(sum[:])
	}
	return basis, nil
}

func groupDigest(deps dependencies, segment document.Segment, result document.Result, stage, model, revision string, context, maxLines int, translations string) (string, error) {
	basis, err := taskBasis(deps, segment, result, stage, model, revision, context, maxLines)
	if err != nil {
		return "", err
	}
	return jsonfile.Digest(struct {
		Basis, Dst, Preceding, Translations string
	}{basis, result.Dst, deps.preceding[segment.ID], translations})
}

func scopedDigest(deps dependencies, segment document.Segment, result document.Result, stage, model, revision string, context, maxLines int, scope, firstID, lastID string) (string, error) {
	scope = scopeName(scope)
	// Completed group tasks from before batch bounds were recorded use the original digest.
	if scope == "group" && firstID == "" && lastID == "" {
		return groupDigest(deps, segment, result, stage, model, revision, context, maxLines, deps.translations[segment.Group])
	}
	if scope != "group" && scope != "task" {
		return "", nil
	}
	job := deps.job
	first, firstOK := job.SegmentIndex(firstID)
	last, lastOK := job.SegmentIndex(lastID)
	target, targetOK := job.SegmentIndex(segment.ID)
	if !firstOK || !lastOK || !targetOK || first > target || target > last || last-first+1 > maxLines || job.Segments[first].Group != segment.Group || job.Segments[last].Group != segment.Group {
		return "", nil
	}
	if scope == "group" {
		lastResult, done := job.Done[lastID]
		if !done || lastResult.Passthrough {
			return "", nil
		}
		// Only translations through the end of this batch can affect its results.
		return groupDigest(deps, segment, result, stage, model, revision, context, maxLines, deps.preceding[lastID]+"\x00"+lastResult.Dst)
	}
	before, following := taskContext(job, first, last, context)
	var block []Line
	for _, other := range job.Segments[first : last+1] {
		current, exists := job.Done[other.ID]
		if other.Group != segment.Group || !exists || current.Passthrough {
			return "", nil
		}
		block = append(block, Line{ID: other.ID, Src: other.Src, Dst: current.Dst})
	}
	basis, err := taskBasis(deps, segment, result, stage, model, revision, context, maxLines)
	if err != nil {
		return "", err
	}
	return jsonfile.Digest(struct {
		Basis         string
		Before, Block []Line
		Following     []document.Segment
	}{basis, before, block, following})
}

func taskContext(job *store.Store, first, last, context int) ([]Line, []document.Segment) {
	group := job.Segments[first].Group
	var before []Line
	for i := first - 1; i >= max(0, first-context); i-- {
		segment := job.Segments[i]
		if segment.Group != group {
			break
		}
		before = append(before, Line{ID: segment.ID, Src: segment.Src, Dst: job.Done[segment.ID].Dst})
	}
	slices.Reverse(before)
	const followingLines = 3
	var following []document.Segment
	for _, segment := range job.Segments[last+1 : min(len(job.Segments), last+1+followingLines)] {
		if segment.Group != group {
			break
		}
		following = append(following, segment)
	}
	return before, following
}
