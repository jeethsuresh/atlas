package cool.gravitas.atlas;
import org.junit.Test;
import static org.junit.Assert.*;
public class NavigationPolicyTest {
 @Test public void onlyPinnedOriginsStayInWebView(){
  assertTrue(NavigationPolicy.isFirstParty("https://maps.gravitas.cool/"));
  assertTrue(NavigationPolicy.isFirstParty("https://gravitas.cool/login"));
  assertFalse(NavigationPolicy.isFirstParty("https://maps.gravitas.cool.evil.test"));
  assertFalse(NavigationPolicy.isFirstParty("https://evil.test@maps.gravitas.cool"));
  assertFalse(NavigationPolicy.isFirstParty("http://maps.gravitas.cool/"));
  assertFalse(NavigationPolicy.isFirstParty("https://maps.gravitas.cool:8080/"));
  assertFalse(NavigationPolicy.isFirstParty("javascript:alert(1)"));
 }
 @Test public void onlyMapOriginCanRequestLocation(){
  assertTrue(NavigationPolicy.mayLocate("https://maps.gravitas.cool/"));
  assertFalse(NavigationPolicy.mayLocate("https://gravitas.cool/"));
  assertFalse(NavigationPolicy.mayLocate("file:///tmp/test.html"));
 }
}
