package eventbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// SpotInterruptionEvent represents an EC2 Spot Instance Interruption Warning from EventBridge
type SpotInterruptionEvent struct {
	Version    string    `json:"version"`
	ID         string    `json:"id"`
	DetailType string    `json:"detail-type"`
	Source     string    `json:"source"`
	Account    string    `json:"account"`
	Time       time.Time `json:"time"`
	Region     string    `json:"region"`
	Detail     struct {
		InstanceID       string `json:"instance-id"`
		InstanceAction   string `json:"instance-action"`
		AvailabilityZone string `json:"availability-zone"`
	} `json:"detail"`
}

// EventBridgeClient handles EventBridge spot interruption events via SQS
type EventBridgeClient struct {
	sqsClient *sqs.Client
	queueURL  string
	region    string
}

// NewEventBridgeClient creates a new EventBridge client that polls SQS for spot interruption events
func NewEventBridgeClient(region, queueURL string) (*EventBridgeClient, error) {
	cfg, err := config.LoadDefaultConfig(context.TODO(), config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &EventBridgeClient{
		sqsClient: sqs.NewFromConfig(cfg),
		queueURL:  queueURL,
		region:    region,
	}, nil
}

// PollForSpotInterruptions polls SQS for spot interruption events from EventBridge
func (c *EventBridgeClient) PollForSpotInterruptions(ctx context.Context) ([]SpotInterruptionEvent, error) {
	// Receive messages from SQS
	result, err := c.sqsClient.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(c.queueURL),
		MaxNumberOfMessages: 10,
		WaitTimeSeconds:     20, // Long polling
		VisibilityTimeout:   30,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to receive messages from SQS: %w", err)
	}

	var events []SpotInterruptionEvent
	var messagesToDelete []types.DeleteMessageBatchRequestEntry

	for _, message := range result.Messages {
		// Parse the EventBridge message
		var event SpotInterruptionEvent
		if err := json.Unmarshal([]byte(*message.Body), &event); err != nil {
			// Log error but continue processing other messages
			continue
		}

		// Validate this is a spot interruption event
		if event.Source == "aws.ec2" && event.DetailType == "EC2 Spot Instance Interruption Warning" {
			events = append(events, event)
		}

		// Mark message for deletion
		messagesToDelete = append(messagesToDelete, types.DeleteMessageBatchRequestEntry{
			Id:            message.MessageId,
			ReceiptHandle: message.ReceiptHandle,
		})
	}

	// Delete processed messages
	if len(messagesToDelete) > 0 {
		_, err := c.sqsClient.DeleteMessageBatch(ctx, &sqs.DeleteMessageBatchInput{
			QueueUrl: aws.String(c.queueURL),
			Entries:  messagesToDelete,
		})
		if err != nil {
			// Log error but don't fail - messages will become visible again
			return events, fmt.Errorf("failed to delete processed messages: %w", err)
		}
	}

	return events, nil
}
