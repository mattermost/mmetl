package intermediate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPruneAttachments(t *testing.T) {
	inter := &Intermediate{
		Posts: []*IntermediatePost{
			{
				Message:     "root",
				Attachments: []string{"bulk-export-attachments/a_ok.png", "bulk-export-attachments/b_failed.png"},
				Replies: []*IntermediatePost{
					{Message: "reply", Attachments: []string{"bulk-export-attachments/c_failed.png", "bulk-export-attachments/d_ok.png"}},
				},
			},
		},
	}

	PruneAttachments(inter, map[string]bool{
		"bulk-export-attachments/b_failed.png": true,
		"bulk-export-attachments/c_failed.png": true,
	})

	assert.Equal(t, []string{"bulk-export-attachments/a_ok.png"}, inter.Posts[0].Attachments)
	assert.Equal(t, []string{"bulk-export-attachments/d_ok.png"}, inter.Posts[0].Replies[0].Attachments)
}

func TestPruneAttachments_NoFailures(t *testing.T) {
	post := &IntermediatePost{Attachments: []string{"bulk-export-attachments/a_ok.png"}}
	inter := &Intermediate{Posts: []*IntermediatePost{post}}

	PruneAttachments(inter, nil)

	assert.Equal(t, []string{"bulk-export-attachments/a_ok.png"}, post.Attachments)
}

func TestPruneAttachments_AllFailed(t *testing.T) {
	inter := &Intermediate{
		Posts: []*IntermediatePost{
			{Attachments: []string{"bulk-export-attachments/a_failed.png"}},
		},
	}

	PruneAttachments(inter, map[string]bool{"bulk-export-attachments/a_failed.png": true})

	assert.Empty(t, inter.Posts[0].Attachments)
}
