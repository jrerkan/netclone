# Üretim öncesi kontrol listesi

Bu proje bir appliance iskeletidir. Gerçek donanımda tam olarak test edilmeden
üretimde kullanılmamalıdır. Aşağıdaki adımları sırayla doğrulayın.

## 1. ISO ve USB

- [ ] `build/build-iso.ps1` (veya `bash build/build-iso.sh`) hatasız tamamlandı.
- [ ] ISO `sha256` doğrulandı.
- [ ] USB'ye yazıldı ve **UEFI** ile boot edildi.
- [ ] Aynı USB **legacy BIOS** ile de boot edildi.
- [ ] Panel otomatik açıldı; `http://<ip>:8080` başka cihazdan erişilebilir.

## 2. İmaj alma

- [ ] Kaynak disk listede doğru görünüyor (model + boyut).
- [ ] NetClone USB diski kilitli görünüyor.
- [ ] Harici hedef disk bağlandı, boş alan doğru gösteriliyor.
- [ ] İmaj alındı, `manifest.json` ve `image.nci` oluştu.
- [ ] `sha256` manifestteki `checksum` ile eşleşiyor (panelde görünür).

## 3. Tek istemci testi

- [ ] İstemci PXE ile boot etti, panelde göründü.
- [ ] Hedef disk doğru seçildi (log: `target disk: ...`).
- [ ] Aktarım bitti, istemci checksum doğruladı.
- [ ] İstemci yeniden başladı ve kurulu sistem açıldı.
- [ ] Disk boyutları farklıysa (hedef daha büyük) bölümler sağlıklı.

## 4. Çoklu istemci testi

- [ ] 3+ istemci aynı anda bağlandı.
- [ ] Sayaç dolunca aktarım **tek** multicast akışı ile başladı
      (`iftop`/`nload` ile doğrulanabilir: sunucu çıkışı ≈ 1× imaj hızı).
- [ ] Tüm istemciler "tamamlandı" durumuna geçti.
- [ ] Bir istemci aktarım sırasında kapatılırsa diğerleri etkilenmedi
      (udpcast yeniden gönderim yapar; gecikme olabilir).
- [ ] "Şimdi Başlat" ve "İptal" düğmeleri beklendiği gibi çalışıyor.

## 5. Ağ senaryoları

- [ ] İzole ağda DHCP sunucu modu.
- [ ] Mevcut DHCP olan ağda proxyDHCP modu.
- [ ] VLAN/switch üzerinden multicast çalışıyor (IGMP snooping sorunsuz).
- [ ] Farklı marka NIC'ler (Intel, Realtek, Broadcom) PXE boot edebiliyor.

## 6. Güvenlik

- [ ] Cihaz yalnızca kurulum ağında; internete açık değil.
- [ ] Fiziksel erişim kontrol altında (panelde kimlik doğrulama yok).
- [ ] İmajların saklandığı disk şifrelenmiş (istenirse LUKS).
- [ ] Oturum token'ları her oturumda yenileniyor.

## Bilinen sınırlamalar

- IPv6, Wi-Fi ve PXE-over-UEFI-HTTP desteklenmez.
- Aynı anda tek oturum ve tek yakalama çalışır.
- İmaj yazımı sırasında hedef diskteki tüm veri silinir; geri alma yoktur.
- `sgdisk -e` yalnızca GPT için yedek başlığı taşır; MBR'de ek işlem gerekmez.
- 254'ten fazla istemci için `expected` sınırını ve dnsmasq havuzunu genişletin.
