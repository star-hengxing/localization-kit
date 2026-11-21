package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

func (s *Store) PolishContextDigests() map[string]string {
	// Review flags and record hashes must not make neighboring reviews invalidate each other.
	type contextItem struct {
		ID, Group, Src, Dst string
		Done                bool
	}
	groups := map[string][]contextItem{}
	for _, other := range s.Segments {
		result, done := s.Done[other.ID]
		groups[other.Group] = append(groups[other.Group], contextItem{ID: other.ID, Group: other.Group, Src: other.Src, Dst: result.Dst, Done: done})
	}
	digests := make(map[string]string, len(groups))
	for group, values := range groups {
		data, _ := json.Marshal(values)
		sum := sha256.Sum256(data)
		digests[group] = hex.EncodeToString(sum[:])
	}
	return digests
}

func (s *Store) PendingPolishReviews() (map[string][]string, error) {
	digests := s.PolishContextDigests()
	var historicalActors map[string][]string
	for _, result := range s.Done {
		if result.Origin == "manual" && result.PolishContextHash == "" && len(result.PolishActors) == 0 && result.PreviousHash != "" {
			var err error
			historicalActors, err = s.PolishActorsByID()
			if err != nil {
				return nil, err
			}
			break
		}
	}
	pending := map[string][]string{}
	for _, segment := range s.Segments {
		result, ok := s.Done[segment.ID]
		if ok && (result.Origin == "polish" || result.Origin == "proofread") && result.PolishCompletion != nil {
			continue
		}
		if ok && len(result.PolishActors) == 0 {
			result.PolishActors = historicalActors[segment.ID]
		}
		if !ok || result.PolishContextHash == "" && result.Origin != "polish" && len(result.PolishActors) == 0 {
			continue
		}
		if result.PolishContextHash != digests[segment.Group] {
			pending[segment.ID] = result.PolishActors
		}
	}
	return pending, nil
}

func (s *Store) RefreshPolishReviews() error {
	pending, err := s.PendingPolishReviews()
	if err != nil {
		return err
	}
	digests := s.PolishContextDigests()
	for _, segment := range s.Segments {
		actors, ok := pending[segment.ID]
		if !ok {
			continue
		}
		result := s.Done[segment.ID]
		result.PolishActors = actors
		if !result.NeedsReview {
			result.ReviewSignature, result.ReviewTermsHash = result.Signature, result.TermsHash
		}
		result.NeedsReview, result.PolishContextReview = true, true
		result.PolishContextHash = digests[segment.Group]
		result.ContextRevision++
		if err := s.Append(result); err != nil {
			return fmt.Errorf("marking polish context for review: %w", err)
		}
	}
	return nil
}
