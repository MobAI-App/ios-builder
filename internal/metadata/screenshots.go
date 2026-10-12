package metadata

import (
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // DecodeConfig for .jpg screenshots
	_ "image/png"  // DecodeConfig for .png screenshots
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaxScreenshotsPerSet is App Store Connect's limit per display type and locale.
const MaxScreenshotsPerSet = 10

// DisplayTypes are the screenshotDisplayType values a subfolder of
// screenshots/<locale>/ may be named after.
var DisplayTypes = []string{
	"APP_IPHONE_67", "APP_IPHONE_65", "APP_IPHONE_61", "APP_IPHONE_58", "APP_IPHONE_55",
	"APP_IPHONE_47", "APP_IPHONE_40", "APP_IPHONE_35",
	"APP_IPAD_PRO_3GEN_129", "APP_IPAD_PRO_3GEN_11", "APP_IPAD_PRO_129", "APP_IPAD_105", "APP_IPAD_97",
	"APP_DESKTOP", "APP_APPLE_TV", "APP_APPLE_VISION_PRO",
	"APP_WATCH_ULTRA", "APP_WATCH_SERIES_10", "APP_WATCH_SERIES_7", "APP_WATCH_SERIES_4", "APP_WATCH_SERIES_3",
	"IMESSAGE_APP_IPHONE_67", "IMESSAGE_APP_IPHONE_65", "IMESSAGE_APP_IPHONE_61", "IMESSAGE_APP_IPHONE_58",
	"IMESSAGE_APP_IPHONE_55", "IMESSAGE_APP_IPHONE_47", "IMESSAGE_APP_IPHONE_40",
	"IMESSAGE_APP_IPAD_PRO_3GEN_129", "IMESSAGE_APP_IPAD_PRO_3GEN_11", "IMESSAGE_APP_IPAD_PRO_129",
	"IMESSAGE_APP_IPAD_105", "IMESSAGE_APP_IPAD_97",
}

// pixelSizes maps a portrait size ("WxH") to the display type it is
// inferred as; landscape images are matched rotated. Sizes several display
// types accept (2048x2732 is both 12.9" iPad generations) go to the newer
// one; a subfolder named after the display type overrides the inference.
var pixelSizes = map[string]string{
	"1320x2868": "APP_IPHONE_67", // 6.9"
	"1290x2796": "APP_IPHONE_67",
	"1260x2736": "APP_IPHONE_67",
	"1284x2778": "APP_IPHONE_65",
	"1242x2688": "APP_IPHONE_65",
	"1206x2622": "APP_IPHONE_61", // 6.3"
	"1179x2556": "APP_IPHONE_61",
	"1170x2532": "APP_IPHONE_61",
	"1125x2436": "APP_IPHONE_58",
	"1080x2340": "APP_IPHONE_58",
	"1242x2208": "APP_IPHONE_55",
	"750x1334":  "APP_IPHONE_47",
	"640x1136":  "APP_IPHONE_40",
	"640x960":   "APP_IPHONE_35",
	"2064x2752": "APP_IPAD_PRO_3GEN_129", // 13"
	"2048x2732": "APP_IPAD_PRO_3GEN_129",
	"1668x2420": "APP_IPAD_PRO_3GEN_11",
	"1668x2388": "APP_IPAD_PRO_3GEN_11",
	"1640x2360": "APP_IPAD_PRO_3GEN_11",
	"1488x2266": "APP_IPAD_PRO_3GEN_11",
	"1668x2224": "APP_IPAD_105",
	"1536x2048": "APP_IPAD_97",
	"800x1280":  "APP_DESKTOP",
	"900x1440":  "APP_DESKTOP",
	"1600x2560": "APP_DESKTOP",
	"1800x2880": "APP_DESKTOP",
}

// DisplayTypeForSize infers the display type from pixel dimensions; "" when unknown.
func DisplayTypeForSize(width, height int) string {
	if width > height {
		width, height = height, width
	}
	return pixelSizes[fmt.Sprintf("%dx%d", width, height)]
}

func knownDisplayType(name string) bool {
	for _, t := range DisplayTypes {
		if t == name {
			return true
		}
	}
	return false
}

func isImage(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg":
		return true
	}
	return false
}

func imageSize(path string) (width, height int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, fmt.Errorf("%s: %w", path, err)
	}
	return cfg.Width, cfg.Height, nil
}

// LocalScreenshots maps locale → display type → image paths in upload order.
type LocalScreenshots map[string]map[string][]string

// LoadScreenshots reads screenshots/<locale>/: images directly inside are
// assigned by pixel size, images in a subfolder named after a display type
// (APP_IPHONE_67, ...) go to that type. Files are ordered by name. Other
// subfolders (fastlane's iMessage, ...) are skipped and returned as warnings.
func LoadScreenshots(dir string) (LocalScreenshots, []string, error) {
	shots := LocalScreenshots{}
	var warnings, problems []string
	locales, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return shots, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	add := func(locale, displayType, path string) {
		if shots[locale] == nil {
			shots[locale] = map[string][]string{}
		}
		shots[locale][displayType] = append(shots[locale][displayType], path)
	}
	for _, l := range locales {
		if !l.IsDir() || strings.HasPrefix(l.Name(), ".") || skippedDirs[l.Name()] {
			continue
		}
		localeDir := filepath.Join(dir, l.Name())
		entries, err := os.ReadDir(localeDir)
		if err != nil {
			return nil, nil, err
		}
		for _, e := range entries {
			path := filepath.Join(localeDir, e.Name())
			switch {
			case strings.HasPrefix(e.Name(), "."):
			case e.IsDir() && knownDisplayType(e.Name()):
				files, err := os.ReadDir(path)
				if err != nil {
					return nil, nil, err
				}
				for _, f := range files {
					if !f.IsDir() && isImage(f.Name()) {
						add(l.Name(), e.Name(), filepath.Join(path, f.Name()))
					}
				}
			case e.IsDir():
				warnings = append(warnings, fmt.Sprintf("skipped %s: not a display type (%s, ...)", path, strings.Join(DisplayTypes[:3], ", ")))
			case isImage(e.Name()):
				w, h, err := imageSize(path)
				if err != nil {
					return nil, nil, err
				}
				displayType := DisplayTypeForSize(w, h)
				if displayType == "" {
					problems = append(problems, fmt.Sprintf("%s is %dx%d, which matches no display type; move it into a subfolder named after one (%s/<DISPLAY_TYPE>/)", path, w, h, localeDir))
					continue
				}
				add(l.Name(), displayType, path)
			}
		}
	}
	for locale, sets := range shots {
		for displayType, paths := range sets {
			sort.Strings(paths)
			if len(paths) > MaxScreenshotsPerSet {
				problems = append(problems, fmt.Sprintf("%s %s has %d screenshots, the limit is %d", locale, displayType, len(paths), MaxScreenshotsPerSet))
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, warnings, fmt.Errorf("screenshots are not valid:\n  %s", strings.Join(problems, "\n  "))
	}
	return shots, warnings, nil
}
