package log

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-lambda-go/lambdacontext"
	"github.com/aws/aws-xray-sdk-go/header"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"os"
	"strings"
)

var log *zap.SugaredLogger
var logConfig Configuration

type Configuration struct {
	logLevel               string
	application            string
	project                string
	projectGroup           string
	version                string
	customAttributesPrefix string
}

func NewConfiguration(logLevel, application, project, projectGroup, version, customAttributesPrefix string) Configuration {
	v := lambdacontext.FunctionVersion
	if version != "" {
		v = version
	}
	return Configuration{
		logLevel:               strings.ToUpper(logLevel),
		application:            strings.ToLower(application),
		project:                strings.ToLower(project),
		projectGroup:           strings.ToLower(projectGroup),
		version:                v,
		customAttributesPrefix: strings.ToLower(customAttributesPrefix),
	}
}

// Customizes logger to unify log format with ec2 application loggers
func Init(config Configuration) {
	logConfig = config
	var logLevel zap.AtomicLevel
	if err := logLevel.UnmarshalText([]byte(config.logLevel)); err != nil {
		fmt.Printf("malformed log level: %+v\n", config.logLevel)
		logLevel = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	rawLogger, _ := zap.Config{
		Level:       logLevel,
		Development: false,
		Encoding:    "json",
		Sampling: &zap.SamplingConfig{
			Initial:    100,
			Thereafter: 100,
		},
		EncoderConfig: zapcore.EncoderConfig{
			TimeKey:        Timestamp,
			LevelKey:       Level,
			NameKey:        "logger",
			CallerKey:      Logger,
			MessageKey:     Message,
			StacktraceKey:  StackTrace,
			LineEnding:     zapcore.DefaultLineEnding,
			EncodeLevel:    zapcore.CapitalLevelEncoder,
			EncodeTime:     zapcore.ISO8601TimeEncoder,
			EncodeDuration: zapcore.SecondsDurationEncoder,
			EncodeCaller:   zapcore.ShortCallerEncoder,
		},
		ErrorOutputPaths: []string{"stderr"},
		OutputPaths:      []string{"stderr"},
	}.Build()

	defer rawLogger.Sync()

	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if len(serviceName) == 0 {
		// check env etc
		serviceName = fmt.Sprintf("%s-%s-%s", config.projectGroup, config.project, config.application)
	}
	log = rawLogger.
		WithOptions(zap.AddCallerSkip(1)).
		With(zap.String(Application, config.application)).
		With(zap.String(Project, config.project)).
		With(zap.String(ProjectGroup, config.projectGroup)).
		With(zap.String(ResourceServiceName, serviceName)).
		With(zap.String(ResourceServiceVersion, config.version)).
		With(zap.String(Version, config.version)).
		Sugar()

	setUpXRay()
}

func SetupTraceIds(ctx context.Context) context.Context {
	spanContext := trace.SpanContextFromContext(ctx)
	if spanContext.IsValid() {
		log = log.
			With(TraceId, spanContext.TraceID().String()).
			With(CorrelationId, spanContext.TraceID().String()).
			With(SpanId, spanContext.SpanID().String()).
			With(TraceFlags, spanContext.TraceFlags().IsSampled())
	} else if traceHeader := getTraceHeaderFromContext(ctx); traceHeader != nil {
		traceId := ToW3C(traceHeader.TraceID)
		log = log.
			With(TraceId, traceId).
			With(CorrelationId, traceId).
			With(SpanId, traceHeader.ParentID).
			With(TraceFlags, traceHeader.SamplingDecision == header.Sampled)
		tId, err := trace.TraceIDFromHex(traceId)
		if err == nil {
			return trace.ContextWithSpanContext(ctx, trace.SpanContext{}.
				WithTraceID(tId))
		}
	}
	return ctx
}

func Flush() error {
	return log.Sync()
}

func Debug(template string, args ...interface{}) {
	log.Debugf(template, args...)
}

func DebugW(msg string, keysAndValues ...interface{}) {
	log.Debugw(msg, keysAndValues...)
}

func Info(template string, args ...interface{}) {
	log.Infof(template, args...)
}

func InfoW(msg string, keysAndValues ...interface{}) {
	log.Infow(msg, keysAndValues...)
}

func Warn(template string, args ...interface{}) {
	log.Warnf(template, args...)
}

func WarnW(msg string, keysAndValues ...interface{}) {
	log.Warnw(msg, keysAndValues...)
}

func Error(template string, args ...interface{}) {
	log.Errorf(template, args...)
}

func ErrorW(msg string, keysAndValues ...interface{}) {
	log.Errorw(msg, keysAndValues...)
}

func With(args ...interface{}) {
	log = log.With(args...)
}

func WithCustomAttr(key string, value interface{}) {
	log = log.With(fmt.Sprintf("Body.%s.%s", logConfig.customAttributesPrefix, key), value)
}

func IsDebugEnabled() bool {
	return log.Desugar().Check(zapcore.DebugLevel, "") != nil
}

func IsInfoEnabled() bool {
	return log.Desugar().Check(zapcore.InfoLevel, "") != nil
}

func IsWarnEnabled() bool {
	return log.Desugar().Check(zapcore.WarnLevel, "") != nil
}

func ToString(value interface{}) string {
	bytes, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%+v", value)
	}
	return string(bytes)
}

func ToW3C(xrayTraceID string) string {
	// Split the X-Ray trace ID into parts
	parts := strings.Split(xrayTraceID, "-")

	// Check if the X-Ray trace ID has the expected number of parts
	if len(parts) != 3 {
		log.Error("invalid X-Ray trace ID format")
		return xrayTraceID
	}

	// Extract the relevant parts for the OpenTelemetry trace ID
	return parts[1] + parts[2]
}
