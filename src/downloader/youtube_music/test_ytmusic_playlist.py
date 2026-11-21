"""Offline checks for the title rules, using real titles from YouTube's Top Weekly Videos Tamil.

Run: python3 test_ytmusic_playlist.py   (needs ytmusicapi installed, makes no network calls)
"""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from ytmusic_playlist import candidates, clean_title, film_of, is_match, performers, resolve, song_name, track_from  # noqa: E402

failed = []


def check(ok):
    """Count instead of stopping at the first failure, so a run shows how many rules hold."""
    check.runs += 1
    if not ok:
        failed.append(sys._getframe(1).f_lineno)


check.runs = 0

# song name = words before the first special character, minus "Video Song"-style words
check(song_name("Kasu Panam Video Song - Extended Version | Soodhu Kavvum | V") == "Kasu Panam")
check(song_name("Bindaas - Lyric Video | Jailer 2| Superstar Rajinikanth | Sun TV") == "Bindaas")
check(song_name("Ennavo Ennavo 8K Song | Priyamaanavale | Thalapathy Vijay") == "Ennavo Ennavo")
check(song_name('Radhimaa [From "Think Indie"] (feat. Sai Smriti)') == "Radhimaa")
check(song_name("Don'u Don'u Don'u (From \"Maari\")") == "Don'u Don'u Don'u")
check(song_name("Pia.. Pia..") == "Pia.. Pia..")
check(song_name("Aura 10/10 (From \"Meesaya Murukku 2\")") == "Aura 10/10")
check(candidates("Love Insurance Kompany - Dheema Promo | Pradeep") == ["Love Insurance Kompany", "Dheema"])

# accept the same song, reject other songs and other versions
check(is_match("Suthudhu Suthudhu Video | Vidyasagar", "Suthudhu Suthudhu", "Suthudhu Suthudhu"))
check(is_match("Vaadi Pottapulla Veliye", "Vaadi Pottapulla Veliye", "Vaadi Pottapulle Veliye"))  # spelling
check(is_match("Hukum", "Hukum", 'Hukum - Thalaivar Alappara - Tamil (From "Jailer")'))
check(not is_match("Raavana Suthudae", "Raavana Suthudae", "Raavana Mavandaa"))
check(not is_match("Rathinamo - Official Music Video", "Rathinamo", "Rathinamo (Female Version)"))
check(not is_match("Koodamela Koodavechi 8K/4K Video Song", "Koodamela Koodavechi", "Koodamela Koodavechi (Rendition)"))
check(not is_match('Tum Tum (From "Enemy - Telugu")', "Tum Tum", 'Tum Tum (From "Enemy - Tamil")'))
check(not is_match("Theri Songs | En Jeevan", "Theri", "Theri Theme"))

# film as album, clean titles keep language
check(film_of('Chola Chola (From "Ponniyin Selvan, Part 1") [Original Motion Picture Soundtrack]') == "Ponniyin Selvan, Part 1")
check(film_of("Annakili (From - Tamizh Kirukkan)") == "Tamizh Kirukkan")
check(film_of('Kar Gayi Chull (From "Kapoor & Sons (Since 1921)")') == "Kapoor & Sons (Since 1921)")
check(clean_title('Hukum - Thalaivar Alappara - Tamil (From "Jailer")') == "Hukum - Thalaivar Alappara - Tamil")
check(clean_title("Gilli (Original Motion Picture Soundtrack)") == "Gilli")

# Tamil-script titles, dashes with a space on one side, singers named in the title
thoothu = "தூது வளை அரச்சு-Thoothu Valai -Mano S Janaki ,Love Duet Melody H D"
check(candidates(thoothu) == ["Thoothu Valai", "தூது வளை அரச்சு"])
check(song_name(thoothu) == "Thoothu Valai")
check(candidates("Gun-in Kadhal") == ["Gun-in Kadhal"])  # a dash inside a word is not a separator
# YouTube Music spells it "Thuthu Valaielle": accept only because the singers are named in the video title
check(is_match(thoothu, "Thoothu Valai", "Thuthu Valaielle", ["Mano, S Janaki"]))
check(not is_match(thoothu, "Thoothu Valai", "Thuthu Valaielle"))
check(is_match(thoothu, "தூது வளை அரச்சு", "Thuthu Valaielle", ["Mano, S Janaki"]))
check(not is_match(thoothu, "தூது வளை அரச்சு", "Thuthu Valaielle"))
check(not is_match(thoothu, "Thoothu Valai", "Thuthu Valaielle", ["Mano Raj"]))  # a different singer
# the singer rule loosens spelling only: other songs and other versions stay rejected
check(not is_match("Raavana Suthudae | Anirudh Ravichander", "Raavana Suthudae", "Raavana Mavandaa", ["Anirudh Ravichander"]))
check(not is_match("Rathinamo | Saurav Srisan", "Rathinamo", "Rathinamo (Female Version)", ["Saurav Srisan"]))



# resolve() against canned YouTube Music search results (real ones from Nov 2026
class FakeYT:
    def __init__(self, results):
        self.results = results

    def search(self, query, filter=None, limit=None):
        return self.results.get(query, [])


def song(title, *artists, video_id=None):
    return {"title": title, "artists": [{"name": a} for a in artists], "videoId": video_id or title}


def video(title, *artists):
    """A user upload, or with artists an official music video (YouTube lists the song's artists on it)."""
    if not artists:
        return {"title": title, "artists": [{"name": "Some Channel"}], "videoType": "MUSIC_VIDEO_TYPE_UGC"}
    return {"title": title, "artists": [{"name": a} for a in artists], "videoType": "MUSIC_VIDEO_TYPE_OMV"}


thuthu = song("Thuthu Valaielle", "Mano, S Janaki")
check(resolve(FakeYT({"Thoothu Valai": [thuthu]}), video(thoothu)) == thuthu)

# the second segment is the film: a song named after the film is not this song, even with its composer named
chinna = video("Chinna Chinna Seithi Solli 4K Video Song | Senthoorapandi | Vijay | Vijayakanth | Deva")
sendhoora = song("Sendhoora Pandikku", "Deva", "Vaali", "S. P. Balasubrahmanyam", "K. S. Chithra")
check(resolve(FakeYT({"Senthoorapandi": [sendhoora]}), chinna) is None)

# an exact spelling beats a looser one further up the results
enakke = video("Enakke Enakkaa Official Video | Full HD | Jeans | A.R.Rahman | Prashanth | Vairamuthu")
compilation = song('Ennake Ennaka (From "Jeans")', "Vairamuthu", "P. Unnikrishnan", "A.R. Rahman")
soundtrack = song("Enakke  Enakkaa", "Unnikrishnan")
check(resolve(FakeYT({"Enakke Enakkaa": [compilation, soundtrack]}), enakke) == soundtrack)

# all-Tamil titles: "தமிழ் பாடல்வரிகள்" (Tamil song lyrics) is not part of the name
check(candidates("ஏக் தோ தீன் தமிழ் பாடல்வரிகள்") == ["ஏக் தோ தீன்"])
check(candidates("அவ என்ன என்ன தமிழ் பாடல்வரிகள் | சூர்யா") == ["அவ என்ன என்ன", "சூர்யா"])
# nothing to compare letter by letter: the top result counts when the video lists its artist ("Yuvanshankar" too
ek_do_teen = song("Ek Do Teen", "Yuvanshankar Raja")
lyric_video = video("ஏக் தோ தீன் தமிழ் பாடல்வரிகள்", "Andrea Jeremiah", "Raghu Kunche", "Yuvan Shankar Raja")
check(resolve(FakeYT({"ஏக் தோ தீன் Andrea Jeremiah": [ek_do_teen]}), lyric_video) == ek_do_teen)
check(resolve(FakeYT({"ஏக் தோ தீன்": [ek_do_teen]}), video("ஏக் தோ தீன் தமிழ் பாடல்வரிகள்")) is None)  # unconfirmed
ava = video("அவ என்ன என்ன தமிழ் பாடல்வரிகள் | சூர்யா", "Prasanna", "Harris Jayaraj", "Karthik", "Thamarai")
ava_enna = song("Ava Enna", "Harris Jayaraj")
check(resolve(FakeYT({"அவ என்ன என்ன Prasanna": [ava_enna]}), ava) == ava_enna)
# "சூர்யா" is the actor: another Harris Jayaraj song found for it must not count
check(resolve(FakeYT({"சூர்யா Prasanna": [song("Anjala", "Harris Jayaraj")]}), ava) is None)

# a theme / background score is not the video's song (real case: Top Weekly, Nov 2026
check(not is_match("Gatta Kusthi 2", "Gatta Kusthi 2", "Gatta Kusthi 2 Theme", ["Sean Roldan"], ["Sean Roldan"]))
check(not is_match("Leo Video", "Leo", "Leo BGM", ["Anirudh Ravichander"], ["Anirudh Ravichander"]))

# B10: a song name that mixes scripts stays whole; split at a script change only at a dash or underscore
power = "#Video | वर्दी के Power | #Raushan Rohi , Srishti Bharti | Ft. Aparna Malik | Rangdari Son"
check("वर्दी के Power" in candidates(power))
check("Power" not in candidates(power))
check(candidates("தூது_வளை_Thoothu_Valai") == ["Thoothu_Valai", "தூது_வளை"])
check(candidates("ஏக் தோ தீன் Ek Do Teen") == ["ஏக் தோ தீன் Ek Do Teen"])

# A1: the part of the title that is the listed artist is never searched as a song (real case, Vietnam chart)
coolkid = video("COOLKID - QUÁ ĐỦ RỒI | Official MV", "COOLKID")
qua_du_roi = song("Quá Đủ Rồi", "COOLKID")
check(resolve(FakeYT({"COOLKID COOLKID": [song("Cool Kids", "Echosmith")],
                      "QUÁ ĐỦ RỒI COOLKID": [qua_du_roi]}), coolkid) == qua_du_roi)
# a song named like its artist, with nothing else in the title, is still searched (real case, Korea chart)
crush = song("CRUSH!", "CRUSH")
check(resolve(FakeYT({"CRUSH! CRUSH": [crush]}), video("CRUSH!", "CRUSH")) == crush)

# A2: an official video's artists don't appear on the found song AND the lengths differ by > 30 s: another song
def timed(track, seconds):
    return {**track, "duration_seconds": seconds}


hong_kong = timed(video("Hong Kong", "The Weeknd"), 214)
check(resolve(FakeYT({"Hong Kong The Weeknd": [timed(song("Hong Kong", "Sgija Keys"), 160)]}), hong_kong) is None)
# either condition alone is normal for the right song: a composer-credited video, or a long intro
check(resolve(FakeYT({"Hong Kong The Weeknd": [timed(song("Hong Kong", "Sgija Keys"), 200)]}), hong_kong) is not None)
check(resolve(FakeYT({"Hong Kong The Weeknd": [timed(song("Hong Kong", "The Weeknd"), 160)]}), hong_kong) is not None)
# the film part is searched first and finds a song named after the film; the song part then finds the right one
jailer = timed(video("JAILER - Hukum Lyric Video | Superstar Rajinikanth", "Anirudh Ravichander"), 230)
hukum = timed(song('Hukum - Thalaivar Alappara (From "Jailer")', "Anirudh Ravichander", "Super Subu"), 208)
check(resolve(FakeYT({"JAILER Anirudh Ravichander": [timed(song("Jailer", "Elegie"), 150)],
                      "Hukum Anirudh Ravichander": [hukum]}), jailer) == hukum)
# D1: a label or TV channel listed as the artist is not the song's artist
bindaas = timed(video("Bindaas - Lyric Video | Jailer 2 | Superstar Rajinikanth | Sun TV", "Sun TV"), 260)
check(resolve(FakeYT({"Bindaas Sun TV": [timed(song("Bindaas", "Anirudh Ravichander"), 190)]}), bindaas) is not None)
# D2/D4: "X official", "X - Topic", "XVEVO", group members and combined names count as the same artist
for listed, credited in [("ธีร์ ทีเร็กซ์ OFFICIAL", "ธีร์ ทีเร็กซ์"), ("Anirudh Ravichander - Topic", "Anirudh Ravichander"),
                         ("TaylorSwiftVEVO", "Taylor Swift"), ("Jeng Big Ass", "Big Ass"),
                         ("Jeyma Monica X Dini Kurnia", "Dini Kurnia"), ("CHAERYEONG (ITZY)", "ITZY"),
                         ("Henrique & Juliano", "Juliano")]:
    track = timed(video("Mutiara", listed), 300)
    check(resolve(FakeYT({f"Mutiara {listed}": [timed(song("Mutiara", credited), 200)]}), track) is not None)
# D3: a date stored as the artist (your Replay mix) is not a performer. It stays in the search: dropping it
# reordered the results and "Daddy Mummy" (Villu) matched the Hindi remake from Bhaag Johnny.
check(performers(["Aug 22, 2026", "Sun TV", "Sony Music South"]) == [])
daddy = timed({**video("Daddy Mummy - 4K Video Song டாடி மம்மி  வீட்டில் இல்ல Villu | Vijay", "Oct 30, 2023"), "videoType": None}, 257)
villu = timed(song("Daddy Mummy", "Devi Sri Prasad", "Mamta Mohandas"), 261)
check(resolve(FakeYT({"Daddy Mummy Oct 30, 2023": [villu], "Daddy Mummy": [timed(song("DADDY MUMMY", "DEVI SRI PRASAD (DSP)", "MM MANASI"), 266), villu]}), daddy) == villu)
# the same singer spelled differently is the same artist (real case: Fadel / Fadl Shaker, 32 s apart)
akendni = timed(video("Akendni Maak", "Fadel Shaker"), 346)
check(resolve(FakeYT({"Akendni Maak Fadel Shaker": [timed(song("Akendni Maak", "Fadl Shaker"), 314)]}), akendni) is not None)

# A3: a cover video stays the cover; it is not swapped for the original (real cases, chart Nov 2026)
firestarter = video("K/DA - FIRESTARTER | Cover by Hebi")
original = song("FIRESTARTER", "K/DA")
check(resolve(FakeYT({"FIRESTARTER": [original], "Cover by Hebi": []}), firestarter) is None)
korean = video("ChaeA - 너의 모든 순간 (원곡 : 성시경)")
check(resolve(FakeYT({"너의 모든 순간": [song("너의 모든 순간", "성시경")]}), korean) is None)
check(resolve(FakeYT({"Hukum": [hukum]}), video("Hukum | Discover Tamil")) == hukum)  # "Discover" is not "cover"
# ...but the cover singer's own release of the cover is the right audio (real case, Taiwan chart)
ling = video("如果我变成回忆（R&B版）｜蓝琳 Ling｜Tank经典情歌女声翻唱｜华语伤感抒情歌曲推荐", "藍琳Ling")
ling_song = song("如果我變成回憶（R&B版）", "藍琳Ling")
check(resolve(FakeYT({"如果我变成回忆（R&B版） 藍琳Ling": [ling_song]}), ling) == ling_song)
check(resolve(FakeYT({"FIRESTARTER Hebi": [original]}), video("K/DA - FIRESTARTER | Cover by Hebi", "Hebi")) is None)

# A4: a version named in the video's brackets must be on the found song too (keep the remix/live video)
check(not is_match("Yes! 東京 (Instrumental)", "Yes! 東京", "Yes! 東京"))
check(not is_match("DIRANTAI (Live Dangdut Session)", "DIRANTAI", "Dirantai"))
check(not is_match("Hukum (Remix) | Jailer", "Hukum", "Hukum"))
check(is_match("Hukum (Live)", "Hukum", "Hukum (Live)"))
check(is_match("Hukum (Full Version)", "Hukum", "Hukum"))  # "Full Version" is the song itself
# words outside brackets describe the upload: "Love Duet Melody" must not reject Thoothu Valai
check(is_match(thoothu, "Thoothu Valai", "Thuthu Valaielle", ["Mano, S Janaki"]))
# A5: version words in other languages (real cases, chart Nov 2026)
check(not is_match("甲乙丙丁 (伴奏)", "甲乙丙丁", "甲乙丙丁"))
check(not is_match("幸福在歌唱", "幸福在歌唱", "幸福在歌唱 (合唱版)"))
check(not is_match("Así fue", "Así fue", "Así fue (En Vivo)"))
check(is_match("Así fue (En Vivo)", "Así fue", "Así fue (En Vivo)"))
check(not is_match("Tudo Ok (Ao Vivo)", "Tudo Ok", "Tudo Ok"))
# the same version in two languages is the same version (real case: "(Live)" video, "(Ao Vivo)" song)
check(is_match("Aproveita Que Eu Tô Brigado (Live)", "Aproveita Que Eu Tô Brigado", "Aproveita Que Eu Tô Brigado (Ao Vivo)"))
check(is_match("Flash (Ao Vivo)", "Flash", "Flash (Live)"))
# fan edits (8D, slowed, sped up, nightcore, bass boosted) are swapped for the official song, never matched to an edit
check(candidates("Hukum 8D Audio | Use Headphones") == ["Hukum", "Use Headphones"])
check(candidates("Hukum Slowed Reverb") == ["Hukum"])
check(candidates("Hukum Sped Up") == ["Hukum"])
check(is_match("Hukum (Slowed + Reverb)", "Hukum", "Hukum"))
check(not is_match("Hukum (Slowed + Reverb)", "Hukum", "Hukum (Slowed + Reverb)"))
check(not is_match("Лампочки", "Лампочки", "Лампочки (Speed Down)"))
check(not is_match("Hukum Nightcore", "Hukum", "Hukum (Nightcore)"))
check(not is_match("Hukum (Lofi)", "Hukum", "Hukum"))  # a lofi version is its own release, not a fan edit

print(f"{check.runs - len(failed)} passed, {len(failed)} failed", "(lines " + ", ".join(map(str, failed)) + ")" if failed else "")
sys.exit(1 if failed else 0)
