/// Which hosts may be reached over plain http (pure Dart, no Flutter import).
library;

import 'dart:io';

/// True in debug builds only (same trick as Flutter's `kDebugMode`: asserts
/// run). `10.0.2.2` is the Android emulator's alias of the developer machine;
/// in a release build it is just another private address and needs https.
final bool isDebugBuild = () {
  var debug = false;
  assert(() {
    debug = true;
    return true;
  }());
  return debug;
}();

/// Loopback host: `localhost`, a loopback address, or (debug builds only, or
/// when [allowEmulator] says so) `10.0.2.2`.
bool isLoopbackHost(String host, {bool? allowEmulator}) {
  if (host == 'localhost') return true;
  if (host == '10.0.2.2') return allowEmulator ?? isDebugBuild;
  final a = InternetAddress.tryParse(host);
  return a != null && a.isLoopback;
}
