/// Client-side state of one session's view, fed by a view stream
/// (`view` + `view.patch`, http-api.md section 4.2).
library;

import 'dart:convert';

import 'json.dart';
import 'models.dart';
import 'sse.dart';

/// What [ViewState.applyPatch] did.
enum PatchOutcome {
  applied,

  /// The patch does not start from the current revision / epoch (or is
  /// malformed): refetch the view with `GET .../view` and call [ViewState.reset].
  needsRefetch,
}

class ViewState {
  ViewState([this.view]);

  /// Current view; null until the first `view` event.
  View? view;

  /// Replaces the state with a full view (`view` event or a REST fetch).
  void reset(View v) => view = v;

  /// Applies an SSE frame. Handles `view` and `view.patch`; every other event
  /// name is ignored (returns null). Returns the outcome for patches and
  /// [PatchOutcome.applied] for full views.
  PatchOutcome? handle(SseEvent e) {
    switch (e.event) {
      case 'view':
        final j = _decode(e.data);
        if (j == null) return PatchOutcome.needsRefetch;
        reset(View.fromJson(j));
        return PatchOutcome.applied;
      case 'view.patch':
        final j = _decode(e.data);
        if (j == null) return PatchOutcome.needsRefetch;
        return applyPatch(ViewStreamPatch.fromJson(j));
    }
    return null;
  }

  /// Applies [p] to the current view:
  ///
  /// 1. drop the lines whose absolute index is below `first_line`;
  /// 2. if there is a splice, delete `delete` lines at relative index `at` and
  ///    insert the new ones there;
  /// 3. set `first_line` and `revision`, then replace every field named in
  ///    `set` (`null` clears `dialog` / `exit_code`).
  ///
  /// The result equals a fresh view of the new revision. A patch is applied
  /// only when `from_revision` equals the current revision and the epoch
  /// matches; otherwise the state is left untouched.
  PatchOutcome applyPatch(ViewStreamPatch p) {
    final cur = view;
    if (cur == null || cur.revision != p.fromRevision || cur.epoch != p.epoch) {
      return PatchOutcome.needsRefetch;
    }
    final drop = p.firstLine - cur.firstLine;
    if (drop < 0) return PatchOutcome.needsRefetch;
    var lines = cur.lines.sublist(
      drop > cur.lines.length ? cur.lines.length : drop,
    );
    if (p.hasSplice) {
      final at = p.spliceAt!;
      if (at < 0 || at + p.spliceDelete > lines.length) {
        return PatchOutcome.needsRefetch;
      }
      lines = [
        ...lines.sublist(0, at),
        ...p.spliceInsert,
        ...lines.sublist(at + p.spliceDelete),
      ];
    }
    var next = cur.copyWithLines(
      firstLine: p.firstLine,
      lines: lines,
      revision: p.revision,
    );
    if (p.set.isNotEmpty) {
      // Same path as a fresh view: merge into the JSON form and parse again.
      final m = next.toJson();
      p.set.forEach((k, v) {
        if (k == 'lines' ||
            k == 'first_line' ||
            k == 'revision' ||
            k == 'epoch' ||
            k == 'name') {
          return; // owned by the patch envelope, never settable
        }
        if (v == null) {
          m.remove(k);
        } else if (k == 'attention' && v is bool) {
          m[k] = {'set': v};
        } else {
          m[k] = v;
        }
      });
      next = View.fromJson(m);
    }
    view = next;
    return PatchOutcome.applied;
  }

  static Json? _decode(String data) {
    try {
      final v = jsonDecode(data);
      return v is Map ? asJson(v) : null;
    } on FormatException {
      return null;
    }
  }
}
