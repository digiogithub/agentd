/// Platform facts used by pairing, behind a small seam for tests.
library;

import 'dart:io';

bool get supportsQrScan => Platform.isAndroid;

/// Default device name sent to the host (it is shown on the host terminal).
String defaultDeviceName() {
  if (Platform.isAndroid) return 'Android phone';
  final h = Platform.localHostname.trim();
  final name = h.isEmpty ? 'agentd app' : h;
  return name.length > 64 ? name.substring(0, 64) : name;
}

String platformName() => Platform.operatingSystem;
