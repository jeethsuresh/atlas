package cool.gravitas.atlas;

import android.Manifest;
import android.app.Activity;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.net.Uri;
import android.os.Bundle;
import android.webkit.CookieManager;
import android.webkit.GeolocationPermissions;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;
import android.view.View;

public class MainActivity extends Activity {
 private WebView web;
 private GeolocationPermissions.Callback locationCallback;
 private String locationOrigin;
 @Override public void onCreate(Bundle savedInstanceState) {
  super.onCreate(savedInstanceState);
  web=new WebView(this);
  FrameLayout root=new FrameLayout(this);root.addView(web);setContentView(root);
  root.setOnApplyWindowInsetsListener((v,insets)->{v.setPadding(insets.getSystemWindowInsetLeft(),insets.getSystemWindowInsetTop(),insets.getSystemWindowInsetRight(),insets.getSystemWindowInsetBottom());return insets;});
  root.requestApplyInsets();
  WebSettings settings=web.getSettings();
  settings.setJavaScriptEnabled(true);settings.setDomStorageEnabled(true);settings.setGeolocationEnabled(true);
  settings.setAllowFileAccess(false);settings.setAllowContentAccess(false);settings.setMixedContentMode(WebSettings.MIXED_CONTENT_NEVER_ALLOW);
  settings.setSafeBrowsingEnabled(true);settings.setUserAgentString(settings.getUserAgentString()+" Atlas/"+BuildConfig.VERSION_NAME);
  CookieManager.getInstance().setAcceptThirdPartyCookies(web,false);
  WebView.setWebContentsDebuggingEnabled(BuildConfig.DEBUG);
  web.setWebViewClient(new WebViewClient(){
   @Override public boolean shouldOverrideUrlLoading(WebView view,WebResourceRequest request){
    if(!request.isForMainFrame())return false;
    Uri u=request.getUrl();if(NavigationPolicy.isFirstParty(u.toString()))return false;
    if("https".equals(u.getScheme()))try{startActivity(new Intent(Intent.ACTION_VIEW,u));}catch(android.content.ActivityNotFoundException ignored){}
    return true;
   }
   @Override public void onReceivedError(WebView view,WebResourceRequest request,WebResourceError error){
    if(request.isForMainFrame()) view.loadDataWithBaseURL("https://maps.gravitas.cool/","<meta name='viewport' content='width=device-width'><body style='font-family:sans-serif;padding:50px 28px;background:#f9faf7;color:#304f46'><h1>Back in a moment.</h1><p>Atlas needs a connection to load your map.</p><a href='https://maps.gravitas.cool/'>Try again</a></body>","text/html","UTF-8",null);
   }
  });
  web.setWebChromeClient(new WebChromeClient(){
   @Override public void onGeolocationPermissionsShowPrompt(String origin,GeolocationPermissions.Callback callback){
    if(!NavigationPolicy.mayLocate(origin)){callback.invoke(origin,false,false);return;}
    if(checkSelfPermission(Manifest.permission.ACCESS_FINE_LOCATION)==PackageManager.PERMISSION_GRANTED||checkSelfPermission(Manifest.permission.ACCESS_COARSE_LOCATION)==PackageManager.PERMISSION_GRANTED){callback.invoke(origin,true,false);return;}
    if(locationCallback!=null)locationCallback.invoke(locationOrigin,false,false);
    locationOrigin=origin;locationCallback=callback;requestPermissions(new String[]{Manifest.permission.ACCESS_FINE_LOCATION,Manifest.permission.ACCESS_COARSE_LOCATION},31);
   }
  });
  if(savedInstanceState==null)web.loadUrl("https://maps.gravitas.cool/");else web.restoreState(savedInstanceState);
 }
 @Override public void onRequestPermissionsResult(int request,String[] permissions,int[] grants){
  super.onRequestPermissionsResult(request,permissions,grants);
  if(request==31&&locationCallback!=null){boolean allowed=false;for(int grant:grants)allowed|=grant==PackageManager.PERMISSION_GRANTED;locationCallback.invoke(locationOrigin,allowed,false);locationCallback=null;}
 }
 @Override protected void onSaveInstanceState(Bundle out){web.saveState(out);super.onSaveInstanceState(out);}
 @Override protected void onPause(){if(locationCallback==null)web.evaluateJavascript("if(typeof stopSharing==='function')stopSharing();",null);CookieManager.getInstance().flush();web.onPause();super.onPause();}
 @Override protected void onResume(){super.onResume();if(web!=null)web.onResume();}
 @Override public void onBackPressed(){if(web.canGoBack())web.goBack();else super.onBackPressed();}
 @Override protected void onDestroy(){if(locationCallback!=null)locationCallback.invoke(locationOrigin,false,false);web.destroy();super.onDestroy();}
}
