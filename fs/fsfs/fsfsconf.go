package fsfs

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/otuschhoff/go-svn/config"
	"github.com/otuschhoff/go-svn/svn"
)

type Compression string

const (
	CompressionNone Compression = "none"
	CompressionZlib Compression = "zlib"
	CompressionLZ4  Compression = "lz4"
)

type Config struct {
	FailStop                 bool
	EnableRepSharing         bool
	EnableDirDeltification   bool
	EnablePropsDeltification bool
	MaxDeltificationWalk     int64
	MaxLinearDeltification   int64
	Compression              Compression
	CompressionLevel         int
	RevPropPackSize          int64
	CompressPackedRevProps   bool
	BlockSize                int64
	L2PPageSize              int64
	P2LPageSize              int64
	VerifyBeforeCommit       bool
}

func defaultConfig(format int) Config {
	compression := CompressionZlib
	if format >= 8 {
		compression = CompressionLZ4
	}
	return Config{
		EnableRepSharing:         true,
		EnableDirDeltification:   true,
		EnablePropsDeltification: true,
		MaxDeltificationWalk:     1023,
		MaxLinearDeltification:   16,
		Compression:              compression,
		CompressionLevel:         5,
		RevPropPackSize:          16 * 1024,
		BlockSize:                64 * 1024,
		L2PPageSize:              8192,
		P2LPageSize:              1024 * 1024,
	}
}

func readConfig(path string, format int) (Config, error) {
	result := defaultConfig(format)
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return Config{}, err
	}
	document, parseErr := config.Parse(file)
	closeErr := file.Close()
	if parseErr != nil {
		return Config{}, fmt.Errorf("%w: parse fsfs.conf: %v", svn.ErrFSCorrupt, parseErr)
	}
	if closeErr != nil {
		return Config{}, closeErr
	}
	if result.FailStop, err = configBool(document, "caches", "fail-stop", result.FailStop); err != nil {
		return Config{}, err
	}
	if result.EnableRepSharing, err = configBool(document, "rep-sharing", "enable-rep-sharing", result.EnableRepSharing); err != nil {
		return Config{}, err
	}
	if result.EnableDirDeltification, err = configBool(document, "deltification", "enable-dir-deltification", result.EnableDirDeltification); err != nil {
		return Config{}, err
	}
	if result.EnablePropsDeltification, err = configBool(document, "deltification", "enable-props-deltification", result.EnablePropsDeltification); err != nil {
		return Config{}, err
	}
	if result.MaxDeltificationWalk, err = configInt(document, "deltification", "max-deltification-walk", result.MaxDeltificationWalk, 0, 1<<31-1); err != nil {
		return Config{}, err
	}
	if result.MaxLinearDeltification, err = configInt(document, "deltification", "max-linear-deltification", result.MaxLinearDeltification, 1, 1<<31-1); err != nil {
		return Config{}, err
	}
	if err = parseCompression(document, format, &result); err != nil {
		return Config{}, err
	}
	if result.RevPropPackSize, err = configInt(document, "packed-revprops", "revprop-pack-size", result.RevPropPackSize/1024, 1, 1<<31-1); err != nil {
		return Config{}, err
	}
	result.RevPropPackSize *= 1024
	if result.CompressPackedRevProps, err = configBool(document, "packed-revprops", "compress-packed-revprops", result.CompressPackedRevProps); err != nil {
		return Config{}, err
	}
	if result.BlockSize, err = configInt(document, "io", "block-size", result.BlockSize/1024, 1, 1<<31-1); err != nil {
		return Config{}, err
	}
	result.BlockSize *= 1024
	if result.L2PPageSize, err = configInt(document, "io", "l2p-page-size", result.L2PPageSize, 1, 1<<31-1); err != nil {
		return Config{}, err
	}
	if result.P2LPageSize, err = configInt(document, "io", "p2l-page-size", result.P2LPageSize/1024, 1, 1<<31-1); err != nil {
		return Config{}, err
	}
	result.P2LPageSize *= 1024
	if !powerOfTwo(result.BlockSize) || !powerOfTwo(result.L2PPageSize) || !powerOfTwo(result.P2LPageSize) || !powerOfTwo(result.MaxLinearDeltification) {
		return Config{}, fmt.Errorf("%w: FSFS page and linear deltification sizes must be powers of two", svn.ErrFSCorrupt)
	}
	if result.VerifyBeforeCommit, err = configBool(document, "debug", "verify-before-commit", result.VerifyBeforeCommit); err != nil {
		return Config{}, err
	}
	return result, nil
}

func configBool(document *config.Document, section, option string, fallback bool) (bool, error) {
	value := strings.ToLower(strings.TrimSpace(document.Get(section, option, "")))
	if value == "" {
		return fallback, nil
	}
	switch value {
	case "yes", "true", "on", "1":
		return true, nil
	case "no", "false", "off", "0":
		return false, nil
	default:
		return fallback, fmt.Errorf("%w: invalid boolean %q for [%s] %s", svn.ErrFSCorrupt, value, section, option)
	}
}

func configInt(document *config.Document, section, option string, fallback, minimum, maximum int64) (int64, error) {
	value := strings.TrimSpace(document.Get(section, option, ""))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < minimum || parsed > maximum {
		return fallback, fmt.Errorf("%w: invalid integer %q for [%s] %s", svn.ErrFSCorrupt, value, section, option)
	}
	return parsed, nil
}

func parseCompression(document *config.Document, format int, result *Config) error {
	value := strings.ToLower(strings.TrimSpace(document.Get("deltification", "compression", "")))
	if value == "" {
		level, err := configInt(document, "deltification", "compression-level", int64(result.CompressionLevel), 0, 9)
		result.CompressionLevel = int(level)
		return err
	}
	switch value {
	case "none":
		result.Compression, result.CompressionLevel = CompressionNone, 0
	case "zlib":
		result.Compression, result.CompressionLevel = CompressionZlib, 5
	case "lz4":
		if format < 8 {
			return fmt.Errorf("%w: lz4 compression requires FSFS format 8", svn.ErrFSUnsupportedFormat)
		}
		result.Compression, result.CompressionLevel = CompressionLZ4, 0
	default:
		if strings.HasPrefix(value, "zlib-") {
			level, err := strconv.Atoi(strings.TrimPrefix(value, "zlib-"))
			if err == nil && level >= 1 && level <= 9 {
				result.Compression, result.CompressionLevel = CompressionZlib, level
				return nil
			}
		}
		return fmt.Errorf("%w: invalid compression %q", svn.ErrFSCorrupt, value)
	}
	return nil
}

func powerOfTwo(value int64) bool { return value > 0 && value&(value-1) == 0 }
