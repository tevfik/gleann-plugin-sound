//go:build linux

package keyboard

/*
#cgo LDFLAGS: -lX11 -lXtst
#include <X11/Xlib.h>
#include <X11/keysym.h>
#include <X11/extensions/XTest.h>
#include <stdio.h>

// Xlib's default error handler exits the process; one bad request from a
// synthetic key event must not kill the dictation daemon.
static int gleannXError(Display *d, XErrorEvent *e) {
	fprintf(stderr, "[keyboard] X error ignored (request %d.%d, code %d)\n",
		e->request_code, e->minor_code, e->error_code);
	return 0;
}

static Display *gleannDpy;

static void gleannReleaseModifiers(void) {
	static const KeySym mods[] = {
		XK_Control_L, XK_Control_R, XK_Shift_L, XK_Shift_R,
		XK_Alt_L, XK_Alt_R, XK_Super_L, XK_Super_R,
		XK_Meta_L, XK_Meta_R, XK_ISO_Level3_Shift,
	};
	if (!gleannDpy) {
		XSetErrorHandler(gleannXError);
		gleannDpy = XOpenDisplay(NULL);
		if (!gleannDpy) return;
	}
	for (unsigned i = 0; i < sizeof mods / sizeof mods[0]; i++) {
		KeyCode kc = XKeysymToKeycode(gleannDpy, mods[i]);
		if (kc == 0) continue; // not on this keyboard layout
		XTestFakeKeyEvent(gleannDpy, kc, False, CurrentTime);
	}
	XFlush(gleannDpy);
}
*/
import "C"

// releaseModifiers sends key-up for every modifier present in the current
// keyboard layout, so injected characters are not combined with the keys the
// user is holding for the hotkey. Keysyms the layout lacks are skipped:
// a key-up for keycode 0 is a BadValue X error, fatal under Xlib's default
// handler, which is why this does not go through robotgo.
func releaseModifiers() { C.gleannReleaseModifiers() }
