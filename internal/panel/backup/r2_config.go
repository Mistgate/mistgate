//go:build !js

package backup

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func loadBackupAWSConfig(ctx context.Context, settings store.PanelBackupSettings, secret string) (aws.Config, error) {
	return config.LoadDefaultConfig(ctx,
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(settings.AccessKeyID, secret, "")),
		config.WithRegion("auto"),
	)
}
