//go:build darwin && cgo

package desktop

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Cocoa -framework WebKit

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#include <stdlib.h>
#include <stdint.h>
#include "_cgo_export.h"

@class ColabHiveCocoaController;

@interface ColabHiveScriptBridge : NSObject <WKScriptMessageHandler>
@property (nonatomic, weak) ColabHiveCocoaController *owner;
@property (nonatomic, assign) BOOL isTray;
@end

@interface ColabHiveCocoaController : NSObject <NSApplicationDelegate, NSWindowDelegate, NSPopoverDelegate, WKNavigationDelegate>
@property (nonatomic, copy) NSString *baseURL;
@property (nonatomic, copy) NSString *mainBootstrapJS;
@property (nonatomic, copy) NSString *trayBootstrapJS;
@property (nonatomic, strong) NSData *iconPNGData;
@property (nonatomic, assign) BOOL startMinimized;
@property (nonatomic, assign) BOOL autostartEnabled;
@property (nonatomic, assign) BOOL isQuitting;
@property (nonatomic, assign) BOOL mainNavigated;
@property (nonatomic, copy) NSString *pendingMainRoute;
@property (nonatomic, copy) NSString *tooltipText;
@property (nonatomic, copy) NSString *poolMenuLabel;

@property (nonatomic, strong) NSWindow *mainWindow;
@property (nonatomic, strong) WKWebView *mainWebView;
@property (nonatomic, strong) NSStatusItem *statusItem;
@property (nonatomic, strong) NSPopover *popover;
@property (nonatomic, strong) WKWebView *trayWebView;
@property (nonatomic, strong) NSEvent *lastMouseDownEvent;
@property (nonatomic, strong) id mouseMonitor;

- (void)handleScriptMessage:(WKScriptMessage *)message fromTray:(BOOL)isTray;
- (void)showMainWindowWithRoute:(NSString *)route;
- (void)hideMainWindow;
- (void)minimizeMainWindow;
- (void)zoomMainWindow;
- (void)showTrayPopover;
- (void)hideTrayPopover;
- (void)toggleTrayPopover;
- (void)evalMainJS:(NSString *)js;
- (void)evalTrayJS:(NSString *)js;
- (void)updateTelemetryTooltip:(NSString *)tip poolLabel:(NSString *)pool autostart:(BOOL)autoOn;
- (void)performGracefulQuit;
@end

@implementation ColabHiveScriptBridge
- (void)userContentController:(WKUserContentController *)userContentController
      didReceiveScriptMessage:(WKScriptMessage *)message {
	(void)userContentController;
	[self.owner handleScriptMessage:message fromTray:self.isTray];
}
@end

static ColabHiveCocoaController *gColabHiveController = nil;

static inline void ch_dispatch_main(dispatch_block_t block) {
	if ([NSThread isMainThread]) {
		block();
	} else {
		dispatch_async(dispatch_get_main_queue(), block);
	}
}

@implementation ColabHiveCocoaController

- (void)applicationDidFinishLaunching:(NSNotification *)notification {
	(void)notification;
	[self installStandardApplicationMenus];
	[self setupMainWindow];
	[self setupStatusBarAndPopover];

	__weak typeof(self) weakSelf = self;
	self.mouseMonitor = [NSEvent addLocalMonitorForEventsMatchingMask:NSEventMaskLeftMouseDown
	                                                          handler:^NSEvent *(NSEvent *event) {
		__strong typeof(weakSelf) strongSelf = weakSelf;
		if (strongSelf != nil && event.window == strongSelf.mainWindow) {
			strongSelf.lastMouseDownEvent = event;
		}
		return event;
	}];

	if (!self.startMinimized) {
		[self showMainWindowWithRoute:@"/"];
	} else {
		[self reportVisibilityState];
	}
}

- (BOOL)applicationShouldHandleReopen:(NSApplication *)theApplication hasVisibleWindows:(BOOL)flag {
	(void)theApplication;
	if (!flag) {
		[self showMainWindowWithRoute:@"/"];
	}
	return YES;
}

- (NSApplicationTerminateReply)applicationShouldTerminate:(NSApplication *)sender {
	(void)sender;
	if (self.isQuitting) {
		return NSTerminateNow;
	}
	colabhiveDarwinOnMenuCommand(1007);
	return NSTerminateCancel;
}

- (void)installStandardApplicationMenus {
	NSMenu *menubar = [[NSMenu alloc] init];

	// 1. Application Menu (ColabHive)
	NSMenuItem *appBarItem = [[NSMenuItem alloc] init];
	[menubar addItem:appBarItem];
	NSMenu *appMenu = [[NSMenu alloc] initWithTitle:@"ColabHive"];
	NSMenuItem *openItem = [[NSMenuItem alloc] initWithTitle:@"打开 ColabHive 主窗口"
	                                                  action:@selector(onMenuAction:)
	                                           keyEquivalent:@"0"];
	openItem.target = self;
	openItem.tag = 1002;
	[appMenu addItem:openItem];
	[appMenu addItem:[NSMenuItem separatorItem]];
	[appMenu addItemWithTitle:@"隐藏 ColabHive" action:@selector(hide:) keyEquivalent:@"h"];
	[appMenu addItem:[NSMenuItem separatorItem]];
	NSMenuItem *quitItem = [[NSMenuItem alloc] initWithTitle:@"退出 ColabHive"
	                                                  action:@selector(onMenuAction:)
	                                           keyEquivalent:@"q"];
	quitItem.target = self;
	quitItem.tag = 1007;
	[appMenu addItem:quitItem];
	appBarItem.submenu = appMenu;

	// 2. Edit Menu (required by WKWebView for Cmd+C / Cmd+V / Cmd+X / Cmd+A / Cmd+Z)
	NSMenuItem *editBarItem = [[NSMenuItem alloc] init];
	[menubar addItem:editBarItem];
	NSMenu *editMenu = [[NSMenu alloc] initWithTitle:@"编辑"];
	[editMenu addItemWithTitle:@"撤销" action:@selector(undo:) keyEquivalent:@"z"];
	[editMenu addItemWithTitle:@"重做" action:@selector(redo:) keyEquivalent:@"Z"];
	[editMenu addItem:[NSMenuItem separatorItem]];
	[editMenu addItemWithTitle:@"剪切" action:@selector(cut:) keyEquivalent:@"x"];
	[editMenu addItemWithTitle:@"拷贝" action:@selector(copy:) keyEquivalent:@"c"];
	[editMenu addItemWithTitle:@"粘贴" action:@selector(paste:) keyEquivalent:@"v"];
	[editMenu addItemWithTitle:@"全选" action:@selector(selectAll:) keyEquivalent:@"a"];
	editBarItem.submenu = editMenu;

	// 3. Window Menu
	NSMenuItem *winBarItem = [[NSMenuItem alloc] init];
	[menubar addItem:winBarItem];
	NSMenu *winMenu = [[NSMenu alloc] initWithTitle:@"窗口"];
	[winMenu addItemWithTitle:@"最小化" action:@selector(performMiniaturize:) keyEquivalent:@"m"];
	[winMenu addItemWithTitle:@"缩放" action:@selector(performZoom:) keyEquivalent:@""];
	[winMenu addItemWithTitle:@"关闭窗口" action:@selector(performClose:) keyEquivalent:@"w"];
	winBarItem.submenu = winMenu;

	[NSApp setMainMenu:menubar];
}

- (void)setupMainWindow {
	NSScreen *screen = [NSScreen mainScreen];
	NSRect vis = screen ? screen.visibleFrame : NSMakeRect(0, 0, 1440, 900);
	CGFloat w = MIN((CGFloat)1380.0, MAX((CGFloat)860.0, vis.size.width - 40.0));
	CGFloat h = MIN((CGFloat)880.0, MAX((CGFloat)560.0, vis.size.height - 40.0));
	CGFloat x = vis.origin.x + (vis.size.width - w) * 0.5;
	CGFloat y = vis.origin.y + (vis.size.height - h) * 0.5;

	NSWindowStyleMask mask = NSWindowStyleMaskTitled
		| NSWindowStyleMaskClosable
		| NSWindowStyleMaskMiniaturizable
		| NSWindowStyleMaskResizable
		| NSWindowStyleMaskFullSizeContentView;

	self.mainWindow = [[NSWindow alloc] initWithContentRect:NSMakeRect(x, y, w, h)
	                                              styleMask:mask
	                                                backing:NSBackingStoreBuffered
	                                                  defer:NO];
	self.mainWindow.title = @"ColabHive";
	self.mainWindow.titlebarAppearsTransparent = YES;
	self.mainWindow.titleVisibility = NSWindowTitleHidden;
	self.mainWindow.movableByWindowBackground = NO;
	self.mainWindow.releasedWhenClosed = NO;
	self.mainWindow.delegate = self;
	[self.mainWindow setContentMinSize:NSMakeSize(860, 560)];
	self.mainWindow.collectionBehavior = NSWindowCollectionBehaviorFullScreenPrimary;
	// Warm Stone `#F7F7F5`
	self.mainWindow.backgroundColor = [NSColor colorWithSRGBRed:247.0/255.0
	                                                      green:247.0/255.0
	                                                       blue:245.0/255.0
	                                                      alpha:1.0];

	WKWebViewConfiguration *cfg = [[WKWebViewConfiguration alloc] init];
	WKUserContentController *ucc = [[WKUserContentController alloc] init];
	if (self.mainBootstrapJS.length > 0) {
		WKUserScript *script = [[WKUserScript alloc] initWithSource:self.mainBootstrapJS
		                                              injectionTime:WKUserScriptInjectionTimeAtDocumentStart
		                                           forMainFrameOnly:YES];
		[ucc addUserScript:script];
	}
	ColabHiveScriptBridge *bridge = [[ColabHiveScriptBridge alloc] init];
	bridge.owner = self;
	bridge.isTray = NO;
	[ucc addScriptMessageHandler:bridge name:@"colabhive"];
	cfg.userContentController = ucc;

	NSView *content = self.mainWindow.contentView;
	self.mainWebView = [[WKWebView alloc] initWithFrame:content.bounds configuration:cfg];
	self.mainWebView.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
	self.mainWebView.navigationDelegate = self;
	@try {
		[self.mainWebView setValue:@(NO) forKey:@"drawsBackground"];
	} @catch (NSException *ignored) {
		(void)ignored;
	}
	[content addSubview:self.mainWebView];

	NSURL *startURL = [NSURL URLWithString:[self.baseURL stringByAppendingString:@"/"]];
	if (startURL != nil) {
		[self.mainWebView loadRequest:[NSURLRequest requestWithURL:startURL]];
	}
}

- (void)setupStatusBarAndPopover {
	self.statusItem = [[NSStatusBar systemStatusBar] statusItemWithLength:NSSquareStatusItemLength];
	NSStatusBarButton *btn = self.statusItem.button;
	if (btn != nil) {
		if (self.iconPNGData.length > 0) {
			NSImage *img = [[NSImage alloc] initWithData:self.iconPNGData];
			if (img != nil) {
				[img setSize:NSMakeSize(22, 22)];
				[img setTemplate:NO];
				btn.image = img;
				[NSApp setApplicationIconImage:[[NSImage alloc] initWithData:self.iconPNGData]];
			}
		}
		if (btn.image == nil) {
			btn.title = @"CH";
		}
		btn.toolTip = self.tooltipText ?: @"ColabHive";
		btn.target = self;
		btn.action = @selector(onStatusBarButtonClicked:);
		[btn sendActionOn:(NSEventMaskLeftMouseUp | NSEventMaskRightMouseUp)];
	}

	WKWebViewConfiguration *trayCfg = [[WKWebViewConfiguration alloc] init];
	WKUserContentController *trayUCC = [[WKUserContentController alloc] init];
	if (self.trayBootstrapJS.length > 0) {
		WKUserScript *trayScript = [[WKUserScript alloc] initWithSource:self.trayBootstrapJS
		                                                  injectionTime:WKUserScriptInjectionTimeAtDocumentStart
		                                               forMainFrameOnly:YES];
		[trayUCC addUserScript:trayScript];
	}
	ColabHiveScriptBridge *trayBridge = [[ColabHiveScriptBridge alloc] init];
	trayBridge.owner = self;
	trayBridge.isTray = YES;
	[trayUCC addScriptMessageHandler:trayBridge name:@"colabhive"];
	trayCfg.userContentController = trayUCC;

	NSViewController *popVC = [[NSViewController alloc] init];
	NSView *container = [[NSView alloc] initWithFrame:NSMakeRect(0, 0, 396, 580)];
	self.trayWebView = [[WKWebView alloc] initWithFrame:container.bounds configuration:trayCfg];
	self.trayWebView.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
	@try {
		[self.trayWebView setValue:@(NO) forKey:@"drawsBackground"];
	} @catch (NSException *ignored) {
		(void)ignored;
	}
	[container addSubview:self.trayWebView];
	popVC.view = container;

	self.popover = [[NSPopover alloc] init];
	self.popover.contentSize = NSMakeSize(396, 580);
	self.popover.behavior = NSPopoverBehaviorTransient;
	self.popover.animates = YES;
	self.popover.contentViewController = popVC;
	self.popover.delegate = self;

	NSURL *trayURL = [NSURL URLWithString:[self.baseURL stringByAppendingString:@"/tray?native=1"]];
	if (trayURL != nil) {
		[self.trayWebView loadRequest:[NSURLRequest requestWithURL:trayURL]];
	}
}

- (void)onStatusBarButtonClicked:(id)sender {
	(void)sender;
	NSEvent *evt = [NSApp currentEvent];
	BOOL isRightClick = (evt != nil) && (
		evt.type == NSEventTypeRightMouseUp ||
		evt.type == NSEventTypeRightMouseDown ||
		(evt.modifierFlags & NSEventModifierFlagControl) != 0
	);
	if (isRightClick) {
		[self showStatusBarContextMenu];
	} else {
		[self toggleTrayPopover];
	}
}

- (void)showStatusBarContextMenu {
	if (self.popover.isShown) {
		[self.popover performClose:nil];
	}
	NSMenu *menu = [[NSMenu alloc] initWithTitle:@"ColabHive"];

	NSMenuItem *itemTray = [[NSMenuItem alloc] initWithTitle:@"打开快捷面板" action:@selector(onMenuAction:) keyEquivalent:@""];
	itemTray.target = self;
	itemTray.tag = 1001;
	[menu addItem:itemTray];

	NSMenuItem *itemMain = [[NSMenuItem alloc] initWithTitle:@"打开 ColabHive 主窗口" action:@selector(onMenuAction:) keyEquivalent:@""];
	itemMain.target = self;
	itemMain.tag = 1002;
	[menu addItem:itemMain];

	[menu addItem:[NSMenuItem separatorItem]];

	NSString *poolTitle = self.poolMenuLabel.length > 0 ? self.poolMenuLabel : @"算力池: 查看账号与实时额度";
	NSMenuItem *itemPool = [[NSMenuItem alloc] initWithTitle:poolTitle action:@selector(onMenuAction:) keyEquivalent:@""];
	itemPool.target = self;
	itemPool.tag = 1003;
	[menu addItem:itemPool];

	NSMenuItem *itemSync = [[NSMenuItem alloc] initWithTitle:@"立即同步云端算力" action:@selector(onMenuAction:) keyEquivalent:@""];
	itemSync.target = self;
	itemSync.tag = 1004;
	[menu addItem:itemSync];

	NSMenuItem *itemMCP = [[NSMenuItem alloc] initWithTitle:@"复制 MCP 地址" action:@selector(onMenuAction:) keyEquivalent:@""];
	itemMCP.target = self;
	itemMCP.tag = 1005;
	[menu addItem:itemMCP];

	[menu addItem:[NSMenuItem separatorItem]];

	NSMenuItem *itemAuto = [[NSMenuItem alloc] initWithTitle:@"登录时自动启动" action:@selector(onMenuAction:) keyEquivalent:@""];
	itemAuto.target = self;
	itemAuto.tag = 1006;
	itemAuto.state = self.autostartEnabled ? NSControlStateValueOn : NSControlStateValueOff;
	[menu addItem:itemAuto];

	[menu addItem:[NSMenuItem separatorItem]];

	NSMenuItem *itemQuit = [[NSMenuItem alloc] initWithTitle:@"退出 ColabHive" action:@selector(onMenuAction:) keyEquivalent:@""];
	itemQuit.target = self;
	itemQuit.tag = 1007;
	[menu addItem:itemQuit];

	if (self.statusItem != nil && self.statusItem.button != nil) {
		self.statusItem.menu = menu;
		[self.statusItem.button performClick:nil];
		self.statusItem.menu = nil;
	}
}

- (void)onMenuAction:(NSMenuItem *)sender {
	if (sender == nil) {
		return;
	}
	colabhiveDarwinOnMenuCommand((int)sender.tag);
}

- (void)handleScriptMessage:(WKScriptMessage *)message fromTray:(BOOL)isTray {
	id body = message.body;
	if ([body isKindOfClass:[NSDictionary class]]) {
		NSDictionary *dict = (NSDictionary *)body;
		id rawAct = dict[@"action"];
		NSString *act = [rawAct isKindOfClass:[NSString class]] ? [(NSString *)rawAct lowercaseString] : @"";
		if (!isTray && ([act isEqualToString:@"window-drag"] || [act isEqualToString:@"start-drag"])) {
			[self performNativeWindowDrag];
		}
		NSError *jsonErr = nil;
		NSData *jsonData = [NSJSONSerialization dataWithJSONObject:dict options:0 error:&jsonErr];
		if (jsonData != nil && jsonErr == nil) {
			NSString *jsonStr = [[NSString alloc] initWithData:jsonData encoding:NSUTF8StringEncoding];
			if (jsonStr != nil) {
				colabhiveDarwinOnWebMessage(isTray ? 1 : 0, [jsonStr UTF8String]);
			}
		}
		return;
	}
	if ([body isKindOfClass:[NSString class]]) {
		NSString *str = (NSString *)body;
		NSString *lower = [str lowercaseString];
		if (!isTray && ([lower isEqualToString:@"window-drag"] || [lower isEqualToString:@"start-drag"])) {
			[self performNativeWindowDrag];
		}
		colabhiveDarwinOnWebMessage(isTray ? 1 : 0, [str UTF8String]);
	}
}

- (void)performNativeWindowDrag {
	if (self.mainWindow == nil) {
		return;
	}
	NSEvent *cur = [NSApp currentEvent];
	NSEvent *dragEvt = (cur != nil && cur.type == NSEventTypeLeftMouseDown) ? cur : self.lastMouseDownEvent;
	if (dragEvt != nil) {
		[self.mainWindow performWindowDragWithEvent:dragEvt];
	}
}

- (void)showMainWindowWithRoute:(NSString *)route {
	if (self.popover.isShown) {
		[self.popover performClose:nil];
	}
	[NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
	if (self.mainWindow != nil) {
		if (self.mainWindow.isMiniaturized) {
			[self.mainWindow deminiaturize:nil];
		}
		[self.mainWindow makeKeyAndOrderFront:nil];
	}
	[NSApp activateIgnoringOtherApps:YES];

	if (route.length > 0 && ![route isEqualToString:@"/"]) {
		if (self.mainNavigated) {
			[self navigateMainSPARoute:route];
		} else {
			self.pendingMainRoute = route;
		}
	}
	[self syncMainWindowZoomState];
	[self reportVisibilityState];
}

- (void)navigateMainSPARoute:(NSString *)route {
	if (self.mainWebView == nil || route.length == 0) {
		return;
	}
	NSData *encoded = [NSJSONSerialization dataWithJSONObject:@[route] options:0 error:nil];
	if (encoded == nil) {
		return;
	}
	NSString *arrJSON = [[NSString alloc] initWithData:encoded encoding:NSUTF8StringEncoding];
	NSString *js = [NSString stringWithFormat:
		@"(function(arr){ var to = arr[0]; try { var cur = window.location.pathname + window.location.search; if (cur !== to) { window.history.pushState(null, '', to); window.dispatchEvent(new CustomEvent('colabhive:navigate', { detail: { to: to } })); } } catch (_) { window.location.href = to; } })(%@);",
		arrJSON
	];
	[self.mainWebView evaluateJavaScript:js completionHandler:nil];
}

- (void)hideMainWindow {
	if (self.mainWindow != nil) {
		[self.mainWindow orderOut:nil];
	}
	[self reportVisibilityState];
}

- (void)minimizeMainWindow {
	if (self.mainWindow != nil) {
		[self.mainWindow miniaturize:nil];
	}
}

- (void)zoomMainWindow {
	if (self.mainWindow != nil) {
		[self.mainWindow zoom:nil];
		[self syncMainWindowZoomState];
	}
}

- (void)syncMainWindowZoomState {
	if (self.mainWindow == nil || self.mainWebView == nil) {
		return;
	}
	BOOL isMax = self.mainWindow.isZoomed || ((self.mainWindow.styleMask & NSWindowStyleMaskFullScreen) != 0);
	NSString *js = [NSString stringWithFormat:
		@"(function(m){ if (window.__COLABHIVE_WINDOW_CONTROLS__) { window.__COLABHIVE_WINDOW_CONTROLS__.maximized = m; } window.dispatchEvent(new CustomEvent('colabhive:window-state', { detail: { maximized: m, platform: 'macos', frameless: true, trafficLightInset: 76 } })); })(%@);",
		isMax ? @"true" : @"false"
	];
	[self.mainWebView evaluateJavaScript:js completionHandler:nil];
}

- (void)showTrayPopover {
	if (self.popover == nil || self.statusItem == nil || self.statusItem.button == nil) {
		return;
	}
	if (!self.popover.isShown) {
		[NSApp activateIgnoringOtherApps:YES];
		[self.popover showRelativeToRect:self.statusItem.button.bounds
		                          ofView:self.statusItem.button
		                   preferredEdge:NSRectEdgeMinY];
	}
	[self reportVisibilityState];
}

- (void)hideTrayPopover {
	if (self.popover != nil && self.popover.isShown) {
		[self.popover performClose:nil];
	}
	[self reportVisibilityState];
}

- (void)toggleTrayPopover {
	if (self.popover != nil && self.popover.isShown) {
		[self hideTrayPopover];
	} else {
		[self showTrayPopover];
	}
}

- (void)evalMainJS:(NSString *)js {
	if (self.mainWebView != nil && js.length > 0) {
		[self.mainWebView evaluateJavaScript:js completionHandler:nil];
	}
}

- (void)evalTrayJS:(NSString *)js {
	if (self.trayWebView != nil && js.length > 0) {
		[self.trayWebView evaluateJavaScript:js completionHandler:nil];
	}
}

- (void)updateTelemetryTooltip:(NSString *)tip poolLabel:(NSString *)pool autostart:(BOOL)autoOn {
	self.tooltipText = tip;
	self.poolMenuLabel = pool;
	self.autostartEnabled = autoOn;
	if (self.statusItem != nil && self.statusItem.button != nil && tip.length > 0) {
		self.statusItem.button.toolTip = tip;
	}
}

- (void)performGracefulQuit {
	if (self.isQuitting) {
		return;
	}
	self.isQuitting = YES;
	if (self.mouseMonitor != nil) {
		[NSEvent removeMonitor:self.mouseMonitor];
		self.mouseMonitor = nil;
	}
	if (self.popover != nil && self.popover.isShown) {
		[self.popover close];
	}
	if (self.statusItem != nil) {
		[[NSStatusBar systemStatusBar] removeStatusItem:self.statusItem];
		self.statusItem = nil;
	}
	if (self.mainWindow != nil) {
		[self.mainWindow close];
	}
	[NSApp stop:nil];
	NSEvent *wakeEvt = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
	                                      location:NSMakePoint(0, 0)
	                                 modifierFlags:0
	                                     timestamp:0
	                                  windowNumber:0
	                                       context:nil
	                                       subtype:0
	                                         data1:0
	                                         data2:0];
	if (wakeEvt != nil) {
		[NSApp postEvent:wakeEvt atStart:YES];
	}
}

- (void)reportVisibilityState {
	int mainVis = (self.mainWindow != nil && self.mainWindow.isVisible) ? 1 : 0;
	int trayVis = (self.popover != nil && self.popover.isShown) ? 1 : 0;
	colabhiveDarwinOnStateChanged(mainVis, trayVis);
}

// NSWindowDelegate
- (BOOL)windowShouldClose:(NSWindow *)sender {
	if (self.isQuitting) {
		return YES;
	}
	[sender orderOut:nil];
	[self reportVisibilityState];
	return NO;
}

- (void)windowDidResize:(NSNotification *)notification {
	(void)notification;
	[self syncMainWindowZoomState];
}

- (void)windowDidEnterFullScreen:(NSNotification *)notification {
	(void)notification;
	[self syncMainWindowZoomState];
}

- (void)windowDidExitFullScreen:(NSNotification *)notification {
	(void)notification;
	[self syncMainWindowZoomState];
}

// NSPopoverDelegate
- (void)popoverWillShow:(NSNotification *)notification {
	(void)notification;
	colabhiveDarwinOnStateChanged((self.mainWindow != nil && self.mainWindow.isVisible) ? 1 : 0, 1);
}

- (void)popoverDidClose:(NSNotification *)notification {
	(void)notification;
	colabhiveDarwinOnStateChanged((self.mainWindow != nil && self.mainWindow.isVisible) ? 1 : 0, 0);
}

// WKNavigationDelegate
- (void)webView:(WKWebView *)webView didFinishNavigation:(WKNavigation *)navigation {
	(void)navigation;
	if (webView == self.mainWebView) {
		self.mainNavigated = YES;
		[self syncMainWindowZoomState];
		if (self.pendingMainRoute.length > 0) {
			NSString *route = self.pendingMainRoute;
			self.pendingMainRoute = nil;
			[self navigateMainSPARoute:route];
		}
	}
}

@end

void ch_cocoa_run(
	const char *baseURL,
	const char *mainBootstrapJS,
	const char *trayBootstrapJS,
	const void *iconPNG,
	int iconPNGLen,
	int startMinimized,
	int autostartOn,
	const char *initialTooltip,
	const char *initialPoolLabel
) {
	@autoreleasepool {
		NSApplication *app = [NSApplication sharedApplication];
		if (startMinimized != 0) {
			[app setActivationPolicy:NSApplicationActivationPolicyAccessory];
		} else {
			[app setActivationPolicy:NSApplicationActivationPolicyRegular];
		}

		ColabHiveCocoaController *ctrl = [[ColabHiveCocoaController alloc] init];
		ctrl.baseURL = baseURL ? [NSString stringWithUTF8String:baseURL] : @"http://127.0.0.1:7860";
		ctrl.mainBootstrapJS = mainBootstrapJS ? [NSString stringWithUTF8String:mainBootstrapJS] : @"";
		ctrl.trayBootstrapJS = trayBootstrapJS ? [NSString stringWithUTF8String:trayBootstrapJS] : @"";
		if (iconPNG != NULL && iconPNGLen > 0) {
			ctrl.iconPNGData = [NSData dataWithBytes:iconPNG length:(NSUInteger)iconPNGLen];
		}
		ctrl.startMinimized = (startMinimized != 0);
		ctrl.autostartEnabled = (autostartOn != 0);
		ctrl.tooltipText = initialTooltip ? [NSString stringWithUTF8String:initialTooltip] : @"ColabHive";
		ctrl.poolMenuLabel = initialPoolLabel ? [NSString stringWithUTF8String:initialPoolLabel] : @"";

		gColabHiveController = ctrl;
		app.delegate = ctrl;
		[app run];
		gColabHiveController = nil;
	}
}

void ch_cocoa_show_main(const char *route) {
	NSString *nsRoute = route ? [NSString stringWithUTF8String:route] : @"/";
	ch_dispatch_main(^{
		[gColabHiveController showMainWindowWithRoute:nsRoute];
	});
}

void ch_cocoa_hide_main(void) {
	ch_dispatch_main(^{
		[gColabHiveController hideMainWindow];
	});
}

void ch_cocoa_minimize_main(void) {
	ch_dispatch_main(^{
		[gColabHiveController minimizeMainWindow];
	});
}

void ch_cocoa_zoom_main(void) {
	ch_dispatch_main(^{
		[gColabHiveController zoomMainWindow];
	});
}

void ch_cocoa_show_tray(void) {
	ch_dispatch_main(^{
		[gColabHiveController showTrayPopover];
	});
}

void ch_cocoa_hide_tray(void) {
	ch_dispatch_main(^{
		[gColabHiveController hideTrayPopover];
	});
}

void ch_cocoa_toggle_tray(void) {
	ch_dispatch_main(^{
		[gColabHiveController toggleTrayPopover];
	});
}

void ch_cocoa_eval_main_js(const char *js) {
	if (js == NULL) return;
	NSString *nsJS = [NSString stringWithUTF8String:js];
	ch_dispatch_main(^{
		[gColabHiveController evalMainJS:nsJS];
	});
}

void ch_cocoa_eval_tray_js(const char *js) {
	if (js == NULL) return;
	NSString *nsJS = [NSString stringWithUTF8String:js];
	ch_dispatch_main(^{
		[gColabHiveController evalTrayJS:nsJS];
	});
}

void ch_cocoa_update_telemetry(const char *tooltip, const char *poolLabel, int autostartOn) {
	NSString *nsTip = tooltip ? [NSString stringWithUTF8String:tooltip] : @"";
	NSString *nsPool = poolLabel ? [NSString stringWithUTF8String:poolLabel] : @"";
	BOOL autoBool = (autostartOn != 0);
	ch_dispatch_main(^{
		[gColabHiveController updateTelemetryTooltip:nsTip poolLabel:nsPool autostart:autoBool];
	});
}

void ch_cocoa_copy_clipboard(const char *text) {
	if (text == NULL) return;
	NSString *nsText = [NSString stringWithUTF8String:text];
	ch_dispatch_main(^{
		NSPasteboard *pb = [NSPasteboard generalPasteboard];
		[pb clearContents];
		[pb setString:nsText forType:NSPasteboardTypeString];
	});
}

void ch_cocoa_open_external(const char *urlStr) {
	if (urlStr == NULL) return;
	NSString *nsURL = [NSString stringWithUTF8String:urlStr];
	ch_dispatch_main(^{
		NSURL *u = [NSURL URLWithString:nsURL];
		if (u != nil && ([u.scheme.lowercaseString isEqualToString:@"http"] || [u.scheme.lowercaseString isEqualToString:@"https"])) {
			[[NSWorkspace sharedWorkspace] openURL:u];
		}
	});
}

void ch_cocoa_quit(void) {
	ch_dispatch_main(^{
		[gColabHiveController performGracefulQuit];
	});
}
*/
import "C"
