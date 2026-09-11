// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	feedbackdomain "github.com/mcuadros/director-engine/domain/feedback"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
)

// FeedbackRequest binds one read-only page to an exact owned pull request and
// current Candidate/base tuple. It exposes no reply or resolution operation.
type FeedbackRequest struct {
	Owner              string                `json:"owner"`
	Name               string                `json:"name"`
	RepositoryID       int64                 `json:"repositoryId"`
	RepositoryNodeID   string                `json:"repositoryNodeId"`
	PullRequestNumber  int64                 `json:"pullRequestNumber"`
	Source             feedbackdomain.Source `json:"source"`
	Page               uint32                `json:"page"`
	PageSize           uint32                `json:"pageSize"`
	CandidateSHA       string                `json:"candidateSha"`
	BaseSHA            string                `json:"baseSha"`
	BindingSHA256      string                `json:"bindingSha256"`
	TaskStoreNowMillis int64                 `json:"taskStoreNowMillis"`
}

type FeedbackPage struct {
	ID                string                `json:"id"`
	Code              string                `json:"code"`
	Source            feedbackdomain.Source `json:"source"`
	RepositoryID      int64                 `json:"repositoryId"`
	RepositoryNodeID  string                `json:"repositoryNodeId"`
	PullRequestNumber int64                 `json:"pullRequestNumber"`
	CandidateSHA      string                `json:"candidateSha"`
	BaseSHA           string                `json:"baseSha"`
	BindingSHA256     string                `json:"bindingSha256"`
	Page              uint32                `json:"page"`
	NextPage          uint32                `json:"nextPage,omitempty"`
	Complete          bool                  `json:"complete"`
	ObservedAtMillis  int64                 `json:"observedAtMillis"`
	MaximumAgeMillis  int64                 `json:"maximumAgeMillis"`
	Items             []feedbackdomain.Item `json:"items"`
	SHA256            string                `json:"sha256"`
}

type FeedbackPort interface {
	ObserveFeedbackPage(context.Context, FeedbackRequest) (FeedbackPage, error)
}

// FeedbackObservationPort is the read-only forge surface used before a
// feedback scan. It intentionally excludes every mutation in Port.
type FeedbackObservationPort interface {
	FeedbackPort
	ObserveRepository(context.Context, RepositoryRequest) (publicationdomain.RepositoryObservation, error)
	ListPullRequests(context.Context, ListRequest) (publicationdomain.PullRequestPage, error)
}

func feedbackPageValue(value FeedbackPage) FeedbackPage { value.SHA256 = ""; return value }

func feedbackPageSHA256(value FeedbackPage) string {
	encoded, err := json.Marshal(feedbackPageValue(value))
	if err != nil {
		panic("marshal fixed GitHub feedback page: " + err.Error())
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func SealFeedbackPage(value FeedbackPage) FeedbackPage {
	value.SHA256 = feedbackPageSHA256(value)
	return value
}

func CurrentFeedbackPage(value FeedbackPage, request FeedbackRequest) bool {
	if value.Code != "ok" || value.ID == "" || value.Source != request.Source || value.RepositoryID != request.RepositoryID ||
		value.RepositoryNodeID != request.RepositoryNodeID || value.PullRequestNumber != request.PullRequestNumber ||
		value.CandidateSHA != request.CandidateSHA || value.BaseSHA != request.BaseSHA || value.BindingSHA256 != request.BindingSHA256 ||
		value.Page != request.Page || value.ObservedAtMillis < 0 || value.ObservedAtMillis > request.TaskStoreNowMillis ||
		value.MaximumAgeMillis <= 0 || value.MaximumAgeMillis > feedbackdomain.MaximumObservationAge ||
		request.TaskStoreNowMillis-value.ObservedAtMillis > value.MaximumAgeMillis || len(value.Items) > int(request.PageSize) ||
		value.SHA256 != feedbackPageSHA256(value) {
		return false
	}
	if value.Complete == (value.NextPage != 0) || value.NextPage != 0 && value.NextPage != value.Page+1 {
		return false
	}
	for _, item := range value.Items {
		if item.Source != value.Source {
			return false
		}
	}
	return true
}
