package cool.gravitas.atlas;

import java.net.URI;

public final class NavigationPolicy {
 private NavigationPolicy() {}
 public static boolean isFirstParty(String raw) {
  try {
   URI u = URI.create(raw);
   return "https".equals(u.getScheme()) && u.getUserInfo() == null && (u.getPort() == -1 || u.getPort() == 443)
     && ("maps.gravitas.cool".equals(u.getHost()) || "gravitas.cool".equals(u.getHost()));
  } catch (IllegalArgumentException e) { return false; }
 }
 public static boolean mayLocate(String raw) {
  try { URI u=URI.create(raw); return isFirstParty(raw) && "maps.gravitas.cool".equals(u.getHost()); }
  catch (IllegalArgumentException e) { return false; }
 }
}
