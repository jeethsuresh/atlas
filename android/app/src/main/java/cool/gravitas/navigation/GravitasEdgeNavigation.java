package cool.gravitas.navigation;

import android.app.Activity;
import android.os.Build;
import android.view.WindowInsets;
import android.window.BackEvent;
import android.window.OnBackAnimationCallback;
import android.window.OnBackInvokedDispatcher;

/** Shared edge navigation contract. Android 14 supplies the actual gesture edge. */
public final class GravitasEdgeNavigation {
    private GravitasEdgeNavigation() {}

    public static void install(Activity activity, Runnable back, Runnable forward) {
        if (Build.VERSION.SDK_INT >= 34) Api34.install(activity, back, forward);
    }

    @android.annotation.TargetApi(34)
    private static final class Api34 {
        static void install(Activity activity, Runnable back, Runnable forward) {
            activity.getOnBackInvokedDispatcher().registerOnBackInvokedCallback(
                OnBackInvokedDispatcher.PRIORITY_OVERLAY, new OnBackAnimationCallback() {
                    private boolean right;
                    @Override public void onBackStarted(BackEvent event) {
                        right = event.getSwipeEdge() == BackEvent.EDGE_RIGHT;
                    }
                    @Override public void onBackCancelled() { right = false; }
                    @Override public void onBackInvoked() {
                        boolean goForward = right;
                        right = false;
                        // The overlay priority prevents other routers from stealing
                        // the gesture. Preserve the keyboard's first Back action.
                        WindowInsets insets = activity.getWindow().getDecorView().getRootWindowInsets();
                        if (insets != null && insets.isVisible(WindowInsets.Type.ime())) {
                            activity.getWindow().getInsetsController().hide(WindowInsets.Type.ime());
                        } else if (goForward) {
                            forward.run();
                        } else {
                            back.run();
                        }
                    }
                });
        }
    }
}
