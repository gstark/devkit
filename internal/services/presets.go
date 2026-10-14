package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/gstark/devkit/internal/manifest"
	"github.com/gstark/devkit/internal/resolve"
)

// prepare writes what a service needs on disk before it starts.
func prepare(service *resolve.Service) error {
	if service.DataDir != "" {
		if err := os.MkdirAll(service.DataDir, 0o755); err != nil {
			return err
		}
	}
	if service.Spec.Preset == manifest.PresetLivekit {
		return writeLivekitConfig(service)
	}
	return nil
}

// postStart finishes setup once the server answers.
func postStart(ctx context.Context, service *resolve.Service) error {
	if service.Spec.Preset == manifest.PresetRustfs {
		return ensureBuckets(ctx, service)
	}
	return nil
}

func writeLivekitConfig(service *resolve.Service) error {
	var b strings.Builder
	loopbackOnly := service.Spec.LoopbackOnly != nil && *service.Spec.LoopbackOnly
	fmt.Fprintf(&b, "port: %d\n", service.Port)
	if loopbackOnly {
		b.WriteString("bind_addresses: [127.0.0.1]\n")
	}
	b.WriteString("rtc:\n")
	fmt.Fprintf(&b, "  tcp_port: %d\n", service.Port+1)
	fmt.Fprintf(&b, "  udp_port: %d\n", service.Port+2)
	// Offer this machine's own addresses only. Browsers on the LAN or the
	// tailnet send media straight to them.
	b.WriteString("  use_external_ip: false\n")
	if service.Spec.LoopbackOnly != nil && *service.Spec.LoopbackOnly {
		// Browser and server share this machine: offer the loopback address
		// only, so ICE does not try the other interfaces first.
		b.WriteString("  enable_loopback_candidate: true\n")
		b.WriteString("  ips:\n")
		b.WriteString("    includes: [127.0.0.1/32]\n")
	}
	b.WriteString("keys:\n")
	fmt.Fprintf(&b, "  %s: %s\n", manifest.LivekitAPIKey, manifest.LivekitAPISecret)
	if service.WebhookURL != "" {
		b.WriteString("webhook:\n")
		fmt.Fprintf(&b, "  api_key: %s\n", manifest.LivekitAPIKey)
		b.WriteString("  urls:\n")
		fmt.Fprintf(&b, "    - %s\n", service.WebhookURL)
	}
	b.WriteString("logging:\n")
	if service.Ephemeral {
		b.WriteString("  level: warn\n")
	} else {
		b.WriteString("  level: info\n")
	}
	if err := os.MkdirAll(filepath.Dir(service.ConfigPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(service.ConfigPath, []byte(b.String()), 0o644)
}

// ensureBuckets creates each bucket and lets browsers on any origin GET, PUT,
// POST, and HEAD, with the ETag readable, which direct and multipart uploads
// need.
func ensureBuckets(ctx context.Context, service *resolve.Service) error {
	client, err := S3Client(ctx, fmt.Sprintf("http://127.0.0.1:%d", service.Port))
	if err != nil {
		return err
	}
	for _, bucket := range service.Buckets {
		_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		if err != nil && !isBucketExists(err) {
			return fmt.Errorf("create bucket %s: %w", bucket, err)
		}
		_, err = client.PutBucketCors(ctx, &s3.PutBucketCorsInput{
			Bucket: aws.String(bucket),
			CORSConfiguration: &types.CORSConfiguration{
				CORSRules: []types.CORSRule{{
					AllowedOrigins: []string{"*"},
					AllowedMethods: []string{"GET", "PUT", "POST", "HEAD"},
					AllowedHeaders: []string{"*"},
					ExposeHeaders:  []string{"ETag"},
					MaxAgeSeconds:  aws.Int32(3000),
				}},
			},
		})
		if err != nil {
			return fmt.Errorf("set CORS on %s: %w", bucket, err)
		}
		if service.PublicRead {
			// Anyone may GET objects: a Gotenberg fetching the HTML to print,
			// say, reaches the bucket by URL with no credentials.
			policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::%s/*"}]}`, bucket)
			if _, err := client.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{Bucket: aws.String(bucket), Policy: aws.String(policy)}); err != nil {
				return fmt.Errorf("set public read on %s: %w", bucket, err)
			}
		}
	}
	return nil
}

// S3Client returns a client for a devkit RustFS at endpoint.
func S3Client(ctx context.Context, endpoint string) (*s3.Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(manifest.RustfsRegion),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(manifest.RustfsAccessKey, manifest.RustfsSecretKey, "")),
	)
	if err != nil {
		return nil, err
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	}), nil
}

func isBucketExists(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "BucketAlreadyOwnedByYou") || strings.Contains(msg, "BucketAlreadyExists")
}
