package llmclient

import (
	"errors"
	"regexp"
	"strings"
)

var (
	// Phone numbers: +7..., 8..., 7... with standard formatting or loose digits
	rePhone = regexp.MustCompile(`(?:\+7|8|7)[\s\-\(]*[0-9]{3}[\s\-\)]*[0-9]{3}[\s\-]*[0-9]{2}[\s\-]*[0-9]{2}`)
	// Passport: 4 digits series and 6 digits number
	rePassport = regexp.MustCompile(`\b\d{4}\s+\d{6}\b`)
	// SNILS: 11 digits (3-3-3 2 or 3-3-3-2)
	reSNILS = regexp.MustCompile(`\b\d{3}[-\s]\d{3}[-\s]\d{3}[-\s]\d{2}\b`)
	// OMS Policy: 16 consecutive digits
	reOMS = regexp.MustCompile(`\b\d{16}\b`)
	// Bank card: 16 digits (4-4-4-4)
	reBankCard = regexp.MustCompile(`\b\d{4}[-\s]\d{4}[-\s]\d{4}[-\s]\d{4}\b`)
	// Date of birth: DD.MM.YYYY, DD/MM/YYYY, DD-MM-YYYY, etc.
	reName = regexp.MustCompile(`(?:[А-ЯЁA-Z][а-яёa-z]+(?:[-’'][А-ЯЁA-Zа-яёa-z]+)?\s+){1,2}[А-ЯЁA-Z][а-яёa-z]+(?:[-’'][А-ЯЁA-Zа-яёa-z]+)?`)
	reDOB  = regexp.MustCompile(`\b\d{1,2}[.\-/]\d{1,2}[.\-/]\d{2,4}\b`)
)

// MaskPII sanitizes sensitive personal data (phones, passports, SNILS, OMS, cards, DOB)
// in local diagnostics. This heuristic is not a privacy boundary; remote NLU
// additionally accepts only a closed vocabulary through RemoteSpeech.
func MaskPII(text string) string {
	if text == "" {
		return ""
	}
	s := rePhone.ReplaceAllString(text, "[ТЕЛЕФОН]")
	s = rePassport.ReplaceAllString(s, "[ПАСПОРТ]")
	s = reSNILS.ReplaceAllString(s, "[СНИЛС]")
	s = reOMS.ReplaceAllString(s, "[ОМС]")
	s = reBankCard.ReplaceAllString(s, "[КАРТА]")
	s = reDOB.ReplaceAllString(s, "[ДАТА]")
	s = reName.ReplaceAllString(s, "[ИМЯ]")
	return s
}

// ErrPrivateSpeech is deliberately generic: never include patient text in errors.
var ErrPrivateSpeech = errors.New("utterance requires local NLU")
var remoteTokens = regexp.MustCompile(`[\p{L}\p{N}_’'-]+`)
var remoteState = regexp.MustCompile(`^STATE_[A-Z_]{1,50}$`)

// RemoteSpeech fails closed in every dialogue state, including the first turn.
// Names, addresses, numbers, spoken dates and unknown words remain local. This
// deliberately limits remote fallback recall instead of claiming regexes can
// recognize every person's name. No vocabulary is built from MIS/patient data.
func RemoteSpeech(text, state string) (string, error) {
	switch state {
	case "STATE_AWAIT_IDENTITY", "STATE_GUARDIAN", "STATE_CONFIRM_CANCEL", "STATE_HOME_REQUEST", "STATE_CONFIRM_HOME", "STATE_REMINDER_IDENTITY":
		return "", ErrPrivateSpeech
	}
	if !remoteState.MatchString(state) {
		return "", ErrPrivateSpeech
	}
	text = strings.ReplaceAll(strings.ToLower(text), "ё", "е")
	tokens := remoteTokens.FindAllString(text, -1)
	if len(tokens) == 0 {
		return "", ErrPrivateSpeech
	}
	for _, token := range tokens {
		if !remoteWords[token] {
			return "", ErrPrivateSpeech
		}
	}
	// Reconstruct exclusively from reviewed tokens, discarding other characters.
	return strings.Join(tokens, " "), nil
}

var remoteWords = func() map[string]bool {
	words := strings.Fields(`а без бы был была были было быть в вам вас ваш ваша ваши ваше во вот все всего вы где да даже давайте для до документы документ документами если еще зачем здравствуйте и из или их к как какая какие какой какую когда кому кого который куда ли либо мне меня могу можно мой моя мое мои мы на надо нам нас не него нее нет ни но нужно нужен нужна нужны о об обратиться обращение от отмена отменить отмените отменять перенос перенести перенесите поменять поменяйте запись записи записан записана записаться записать запишите записывайте прием приема приеме принять принял приняла врач врача врачи врачей врачу врачам доктора доктору докторов доктор специалист специалиста специалисты специалистов специальности специальность направления направление направлению консультация консультацию регистратура регистратуры регистратуру поликлиника поликлиники поликлинике поликлинику у узнать узнавать уточнить уточните уточнения узнать расписание расписания расписанию расписании график графика графику работает работаете работают работы работа часов часы во сколько открыто открыта открываетесь закрываетесь закрываются закрывается двери вход входа адрес адресу улица улице найти искать попасть добраться дойти пройти подскажите скажите расскажите объясните объяснять повторите повторить почему чего что чтобы чем это этого эту этой тот такое так там тут только тоже тогда сейчас сегодня завтра послезавтра утро утром день днем вечер вечером после перед обеда обед по про пожалуйста спасибо большое хорошо отлично подходит подходит ли согласен согласна подтверждаю подтверждать подтверждение подтверждения нужно нужна нужны нужен времени время свободное свободные свободных ближайшее ближайший ближайшие другой другое другие раньше позже доступно доступны есть имеется имеются принимают принимаете занимаются оказываете получить получения принести приносить приходить приходят приходить с собой паспорт полис бумага бумаги бумагами прикрепление прикрепления прикрепиться прикреплен прикреплена платно платный платные стоимость цена цены услуг услуги анализ анализы анализов результатов результаты результат готов готовы готовность крови кровь забор сдавать сдачи сдать мочи моча общий общие биохимия биохимии гормоны гормонов пцр посев посева цитология гистология терапевт терапевта терапевту терапевты педиатр педиатра педиатру педиатры хирург хирурга хирургу хирурги офтальмолог офтальмолога офтальмологу окулист окулиста окулисту глаза глазам лор лору оториноларинголог оториноларинголога оториноларингологу кардиолог кардиологу невролог неврологу эндокринолог эндокринологу гинеколог гинекологу стоматолог стоматологу дерматолог дерматологу оператор оператора оператору человек человека человеком сотрудник сотрудника сотруднику живой живого живым соедините переведите позовите неясно непонятно неудобно удобно назначенное стало нужно требуется хочу хочется хотел хотела хотелось бы сначала начать вообще у вас обращаться обратиться можете поможете помощь помочь мне интересует интересуют доступные доступны информацию информация справку справка порядок режим режиму вопрос вопросы медицинский медицинские медицинскую совет лечение лечить диагноз таблетки лекарство симптомы жалобы самочувствие болит боль здоровье температура ребенок ребенка детский детскому взрослый взрослому для нас но еще с со за через между`)
	words = append(words, "сориентируйте")
	out := make(map[string]bool, len(words))
	for _, word := range words {
		out[word] = true
	}
	return out
}()
