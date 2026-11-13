/// `agentd://` deep links (frontend-app.md section 6.3), faked in tests.
library;

import 'package:app_links/app_links.dart';

abstract class DeepLinkSource {
  /// The link that launched the app, if any.
  Future<Uri?> initial();

  /// Links received while running.
  Stream<Uri> get links;
}

class AppLinksSource implements DeepLinkSource {
  AppLinksSource([AppLinks? links]) : _l = links ?? AppLinks();
  final AppLinks _l;

  @override
  Future<Uri?> initial() async {
    try {
      return await _l.getInitialLink();
    } on Object {
      return null;
    }
  }

  @override
  Stream<Uri> get links => _l.uriLinkStream;
}
